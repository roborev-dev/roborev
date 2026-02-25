package storage

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

//go:embed schemas/postgres_v1.sql
var postgresV1Schema string

const defaultTestMachineID = "11111111-1111-1111-1111-111111111111"

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
	pool := openTestPgPool(t)
	ctx := t.Context()

	// Clean up test data - use valid UUIDs
	machineID := uuid.NewString()
	otherMachineID := uuid.NewString()
	jobUUID1 := uuid.NewString()
	jobUUID2 := uuid.NewString()
	reviewUUID1 := uuid.NewString()
	reviewUUID2 := uuid.NewString()

	var repoIDs []int64
	defer func() {
		cleanupTestData(t, pool, machineID, otherMachineID, repoIDs, []string{jobUUID1, jobUUID2})
	}()

	// Register both machines
	if err := pool.RegisterMachine(ctx, machineID, "test"); err != nil {
		t.Fatalf("RegisterMachine failed: %v", err)
	}
	if err := pool.RegisterMachine(ctx, otherMachineID, "other"); err != nil {
		t.Fatalf("RegisterMachine (other) failed: %v", err)
	}

	// Create a repo using the helper
	repoIdentity := "test-repo-" + time.Now().Format("20060102150405")
	repoID := createTestRepo(t, pool.Pool(), TestRepoOpts{Identity: repoIdentity})
	repoIDs = append(repoIDs, repoID)

	// Create a commit using the helper
	commitID := createTestCommit(t, pool.Pool(), TestCommitOpts{RepoID: repoID, SHA: "abc123456789"})

	// Create two jobs with different UUIDs using explicit timestamps
	createTestJob(t, pool.pool, TestJobOpts{
		UUID:            jobUUID1,
		RepoID:          repoID,
		CommitID:        commitID,
		SourceMachineID: machineID,
	})
	createTestJob(t, pool.pool, TestJobOpts{
		UUID:            jobUUID2,
		RepoID:          repoID,
		CommitID:        commitID,
		SourceMachineID: machineID,
	})

	baseTime := time.Now().Truncate(time.Millisecond)
	// Create reviews with explicit timestamps to ensure ordering
	createTestReview(t, pool.pool, TestReviewOpts{
		UUID:               reviewUUID1,
		JobUUID:            jobUUID1,
		UpdatedByMachineID: otherMachineID,
		CreatedAt:          baseTime,
		UpdatedAt:          baseTime,
	})

	createTestReview(t, pool.pool, TestReviewOpts{
		UUID:               reviewUUID2,
		JobUUID:            jobUUID2,
		UpdatedByMachineID: otherMachineID,
		CreatedAt:          baseTime.Add(100 * time.Millisecond), // Explicitly later
		UpdatedAt:          baseTime.Add(100 * time.Millisecond),
	})

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

// openTestPgPool connects to the test Postgres, ensures schema, and registers cleanup.
func openTestPgPool(t *testing.T) *PgPool {
	t.Helper()
	connString := getTestPostgresURL(t)
	ctx := t.Context()
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}
	return pool
}

func cleanupTestData(t *testing.T, pool *PgPool, machineID, otherMachineID string, repoIDs []int64, jobUUIDs []string) {
	t.Helper()
	ctx := t.Context()
	// Clean up in reverse dependency order using tracked UUIDs
	// Delete responses and reviews by job_uuid since that's tracked
	for _, jobUUID := range jobUUIDs {
		pool.pool.Exec(ctx, `DELETE FROM responses WHERE job_uuid = $1`, jobUUID)
		pool.pool.Exec(ctx, `DELETE FROM reviews WHERE job_uuid = $1`, jobUUID)
	}
	pool.pool.Exec(ctx, `DELETE FROM review_jobs WHERE source_machine_id = $1`, machineID)
	pool.pool.Exec(ctx, `DELETE FROM commits WHERE repo_id = ANY($1)`, repoIDs)
	pool.pool.Exec(ctx, `DELETE FROM repos WHERE id = ANY($1)`, repoIDs)
	pool.pool.Exec(ctx, `DELETE FROM machines WHERE machine_id = $1`, machineID)
	pool.pool.Exec(ctx, `DELETE FROM machines WHERE machine_id = $1`, otherMachineID)
}

func TestIntegration_EnsureSchema_AutoInitializesVersion(t *testing.T) {
	// This test verifies that EnsureSchema auto-initializes when schema_version table is empty
	pool := openTestPgPool(t)
	ctx := t.Context()

	// Clear schema_version to simulate empty table
	_, _ = pool.pool.Exec(ctx, `DELETE FROM schema_version`)

	// EnsureSchema should succeed and auto-initialize
	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Verify version was inserted
	var version int
	if err := pool.pool.QueryRow(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("Failed to query version: %v", err)
	}
	if version != pgSchemaVersion {
		t.Errorf("Expected schema version %d, got %d", pgSchemaVersion, version)
	}
}

func TestIntegration_EnsureSchema_RejectsNewerVersion(t *testing.T) {
	// This test verifies that EnsureSchema returns error when schema version is newer than supported
	pool := openTestPgPool(t)
	ctx := t.Context()

	// Insert a newer version
	futureVersion := pgSchemaVersion + 10
	_, err := pool.pool.Exec(ctx, `INSERT INTO schema_version (version) VALUES ($1) ON CONFLICT (version) DO NOTHING`, futureVersion)
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

	// First, check if schema exists
	env := NewMigrationTestEnv(t)
	var schemaExists bool
	if err := env.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.schemata WHERE schema_name = 'roborev')`).Scan(&schemaExists); err != nil {
		t.Fatalf("Failed to check if schema exists: %v", err)
	}

	if schemaExists {
		// Don't actually drop if it has data - just verify the bootstrap works
		pool := openTestPgPool(t)

		// Verify schema_version table is accessible
		var version int
		err := pool.pool.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version)
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
		env.DropSchema("roborev")
		env.CleanupDropSchema("roborev")

		// Fresh database - NewPgPool should succeed with AfterConnect bootstrap
		pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
		if err != nil {
			t.Fatalf("Failed to connect on fresh database: %v", err)
		}
		t.Cleanup(func() { pool.Close() })

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

func setupMigrationEnv(t *testing.T) *MigrationTestEnv {
	env := NewMigrationTestEnv(t)
	// Clean up after test
	env.CleanupDropSchema("roborev")
	env.CleanupDropTable("public", "schema_version")
	env.CleanupDropTable("public", "repos")

	env.DropTable("public", "repos")
	env.DropTable("public", "schema_version")
	env.DropSchema("roborev")
	return env
}

func countSuccesses(success []bool) int {
	count := 0
	for _, ok := range success {
		if ok {
			count++
		}
	}
	return count
}

func TestIntegration_EnsureSchema_MigratesLegacyTables(t *testing.T) {
	// This test verifies that tables in public schema are migrated to roborev
	ctx := t.Context()

	env := setupMigrationEnv(t)
	env.SkipIfTableInSchema("roborev", "schema_version")
	env.SkipIfTableInSchema("public", "schema_version")

	// Create legacy table in public schema
	env.Exec(`CREATE TABLE IF NOT EXISTS public.schema_version (version INTEGER PRIMARY KEY)`)
	env.Exec(`INSERT INTO public.schema_version (version) VALUES (1) ON CONFLICT DO NOTHING`)

	// Now connect with the normal pool and run EnsureSchema
	connString := getTestPostgresURL(t)
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Verify legacy table was migrated (no longer in public)
	var publicExists bool
	if err := pool.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'schema_version'
		)
	`).Scan(&publicExists); err != nil {
		t.Fatalf("Failed to check if public.schema_version exists: %v", err)
	}
	if publicExists {
		t.Error("Expected public.schema_version to NOT exist")
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
	ctx := t.Context()

	env := setupMigrationEnv(t)

	env.SkipIfTableInSchema("roborev", "schema_version")
	env.SkipIfTableInSchema("public", "schema_version")

	// Create roborev schema for mixed state test
	env.Exec(`CREATE SCHEMA IF NOT EXISTS roborev`)

	// Create legacy tables in public schema (simulating old installation)
	env.Exec(`CREATE TABLE IF NOT EXISTS public.schema_version (version INTEGER PRIMARY KEY)`)
	env.Exec(`INSERT INTO public.schema_version (version) VALUES (1) ON CONFLICT DO NOTHING`)

	// Create repos table in public (second legacy table)
	env.Exec(`CREATE TABLE IF NOT EXISTS public.repos (id SERIAL PRIMARY KEY, identity TEXT UNIQUE NOT NULL)`)
	env.Exec(`INSERT INTO public.repos (identity) VALUES ('test-repo-legacy') ON CONFLICT DO NOTHING`)

	// Create machines table directly in roborev (simulating partial migration)
	env.Exec(`CREATE TABLE IF NOT EXISTS roborev.machines (id SERIAL PRIMARY KEY, machine_id UUID UNIQUE NOT NULL, name TEXT)`)

	// Now connect with the normal pool and run EnsureSchema
	connString := getTestPostgresURL(t)
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	if err := pool.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	// Verify public tables gone
	var exists bool
	if err := pool.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='schema_version')`).Scan(&exists); err != nil {
		t.Fatalf("Failed to check public.schema_version: %v", err)
	}
	if exists {
		t.Error("Expected public.schema_version to be gone")
	}
	if err := pool.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='repos')`).Scan(&exists); err != nil {
		t.Fatalf("Failed to check public.repos: %v", err)
	}
	if exists {
		t.Error("Expected public.repos to be gone")
	}

	// Verify roborev.machines exists
	if err := pool.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='roborev' AND table_name='machines')`).Scan(&exists); err != nil {
		t.Fatalf("Failed to check roborev.machines: %v", err)
	}
	if !exists {
		t.Error("Expected roborev.machines to exist")
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
	ctx := t.Context()

	env := setupMigrationEnv(t)

	// Create roborev schema with repos table
	env.Exec(`CREATE SCHEMA IF NOT EXISTS roborev`)
	env.Exec(`CREATE TABLE roborev.repos (id SERIAL PRIMARY KEY, identity TEXT UNIQUE NOT NULL)`)

	// Create public.schema_version so migrateLegacyTables detects the legacy schema
	env.Exec(`CREATE TABLE public.schema_version (version INTEGER PRIMARY KEY)`)

	// Create public.repos table with data
	env.Exec(`CREATE TABLE public.repos (id SERIAL PRIMARY KEY, identity TEXT UNIQUE NOT NULL)`)
	env.Exec(`INSERT INTO public.repos (identity) VALUES ('legacy-repo')`)

	// Now connect and try EnsureSchema - should fail
	connString := getTestPostgresURL(t)
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
	ctx := t.Context()

	env := setupMigrationEnv(t)

	// Create roborev schema with repos table containing data
	env.Exec(`CREATE SCHEMA IF NOT EXISTS roborev`)
	env.Exec(`CREATE TABLE roborev.repos (id SERIAL PRIMARY KEY, identity TEXT UNIQUE NOT NULL)`)
	env.Exec(`INSERT INTO roborev.repos (identity) VALUES ('new-repo')`)

	// Create public.schema_version so migrateLegacyTables detects the legacy schema
	env.Exec(`CREATE TABLE public.schema_version (version INTEGER PRIMARY KEY)`)

	// Create empty public.repos table
	env.Exec(`CREATE TABLE public.repos (id SERIAL PRIMARY KEY, identity TEXT UNIQUE NOT NULL)`)
	// Note: no data inserted - empty table

	// Now connect and run EnsureSchema - should succeed and drop empty public.repos
	connString := getTestPostgresURL(t)
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	err = pool.EnsureSchema(ctx)
	if err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	var exists bool
	if err := pool.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='repos')`).Scan(&exists); err != nil {
		t.Fatalf("Failed to check public.repos: %v", err)
	}
	if exists {
		t.Error("Expected public.repos to be gone")
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
	ctx := t.Context()

	env := setupMigrationEnv(t)

	// Create roborev schema but NOT the repos table
	env.Exec(`CREATE SCHEMA IF NOT EXISTS roborev`)

	// Create public.schema_version so migrateLegacyTables detects the legacy schema
	env.Exec(`CREATE TABLE public.schema_version (version INTEGER PRIMARY KEY)`)

	// Create public.repos table with data
	env.Exec(`CREATE TABLE public.repos (id SERIAL PRIMARY KEY, identity TEXT UNIQUE NOT NULL)`)
	env.Exec(`INSERT INTO public.repos (identity) VALUES ('migrated-repo-1'), ('migrated-repo-2')`)

	// Now connect and run EnsureSchema - should migrate public.repos to roborev.repos
	connString := getTestPostgresURL(t)
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	err = pool.EnsureSchema(ctx)
	if err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	var exists bool
	if err := pool.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='repos')`).Scan(&exists); err != nil {
		t.Fatalf("Failed to check public.repos: %v", err)
	}
	if exists {
		t.Error("Expected public.repos to be gone")
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
	pool := openTestPgPool(t)
	ctx := t.Context()

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
	pool := openTestPgPool(t)
	ctx := t.Context()

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
	job, err := sqliteDB.EnqueueJob(EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "test-sha", Agent: "test", Reasoning: "thorough"})
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

func TestIntegration_BatchUpsertJobs(t *testing.T) {
	pool := openTestPgPool(t)
	ctx := t.Context()

	// Create a test repo
	repoID := createTestRepo(t, pool.Pool(), TestRepoOpts{Identity: "https://github.com/test/batch-jobs-test.git"})

	// Create multiple jobs with prepared IDs
	var jobs []JobWithPgIDs
	for i := range 5 {
		commitID := createTestCommit(t, pool.Pool(), TestCommitOpts{RepoID: repoID, SHA: fmt.Sprintf("batch-jobs-sha-%d", i)})
		jobs = append(jobs, JobWithPgIDs{
			Job: SyncableJob{
				UUID:            uuid.NewString(),
				RepoIdentity:    "https://github.com/test/batch-jobs-test.git",
				CommitSHA:       fmt.Sprintf("batch-jobs-sha-%d", i),
				GitRef:          "test-ref",
				Agent:           "test",
				Status:          "done",
				SourceMachineID: defaultTestMachineID,
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
	if got := countSuccesses(success); got != 5 {
		t.Errorf("Expected 5 jobs upserted, got %d", got)
	}

	// Verify jobs exist
	var jobCount int
	err = pool.pool.QueryRow(ctx, `SELECT COUNT(*) FROM review_jobs WHERE source_machine_id = $1`, defaultTestMachineID).Scan(&jobCount)
	if err != nil {
		t.Fatalf("Count query failed: %v", err)
	}
	if jobCount < 5 {
		t.Errorf("Expected at least 5 jobs in database, got %d", jobCount)
	}

	t.Run("empty batch is no-op", func(t *testing.T) {
		success, err := pool.BatchUpsertJobs(ctx, []JobWithPgIDs{})
		if err != nil {
			t.Errorf("BatchUpsertJobs with empty slice failed: %v", err)
		}
		if success != nil {
			t.Errorf("Expected nil for empty batch, got %v", success)
		}
	})
}

func TestIntegration_BatchUpsertReviews(t *testing.T) {
	pool := openTestPgPool(t)
	ctx := t.Context()

	repoID := createTestRepo(t, pool.Pool(), TestRepoOpts{Identity: "https://github.com/test/batch-reviews-test.git"})
	commitID := createTestCommit(t, pool.Pool(), TestCommitOpts{RepoID: repoID, SHA: "batch-reviews-sha"})
	jobUUID := uuid.NewString()

	createTestJob(t, pool.pool, TestJobOpts{
		UUID:            jobUUID,
		RepoID:          repoID,
		CommitID:        commitID,
		SourceMachineID: defaultTestMachineID,
	})

	reviews := []SyncableReview{
		{
			UUID:               uuid.NewString(),
			JobUUID:            jobUUID,
			Agent:              "test",
			Prompt:             "test prompt 1",
			Output:             "test output 1",
			Addressed:          false,
			UpdatedByMachineID: defaultTestMachineID,
			CreatedAt:          time.Now(),
		},
		{
			UUID:               uuid.NewString(),
			JobUUID:            jobUUID,
			Agent:              "test",
			Prompt:             "test prompt 2",
			Output:             "test output 2",
			Addressed:          true,
			UpdatedByMachineID: defaultTestMachineID,
			CreatedAt:          time.Now(),
		},
	}

	success, err := pool.BatchUpsertReviews(ctx, reviews)
	if err != nil {
		t.Fatalf("BatchUpsertReviews failed: %v", err)
	}
	if got := countSuccesses(success); got != 2 {
		t.Errorf("Expected 2 reviews upserted, got %d", got)
	}

	t.Run("empty batch is no-op", func(t *testing.T) {
		success, err := pool.BatchUpsertReviews(ctx, []SyncableReview{})
		if err != nil {
			t.Errorf("BatchUpsertReviews with empty slice failed: %v", err)
		}
		if success != nil {
			t.Errorf("Expected nil for empty batch, got %v", success)
		}
	})

	t.Run("partial failure with invalid FK", func(t *testing.T) {
		validReviewUUID := uuid.NewString()
		reviews := []SyncableReview{
			{
				UUID:               validReviewUUID,
				JobUUID:            jobUUID, // Valid FK
				Agent:              "test",
				Prompt:             "valid review",
				Output:             "output",
				UpdatedByMachineID: defaultTestMachineID,
				CreatedAt:          time.Now(),
			},
			{
				UUID:               uuid.NewString(),
				JobUUID:            "00000000-0000-0000-0000-000000000000", // Invalid FK - will fail
				Agent:              "test",
				Prompt:             "invalid review",
				Output:             "output",
				UpdatedByMachineID: defaultTestMachineID,
				CreatedAt:          time.Now(),
			},
		}

		success, err := pool.BatchUpsertReviews(ctx, reviews)

		if err == nil {
			t.Error("Expected error from batch with invalid FK, got nil")
		}
		if len(success) != 2 {
			t.Fatalf("Expected success slice length 2, got %d", len(success))
		}
		if !success[0] {
			t.Error("Expected success[0]=true (valid FK)")
		}
		if success[1] {
			t.Error("Expected success[1]=false (invalid FK)")
		}

		var count int
		err = pool.pool.QueryRow(ctx, `SELECT COUNT(*) FROM reviews WHERE uuid = $1`, validReviewUUID).Scan(&count)
		if err != nil {
			t.Fatalf("Failed to query review: %v", err)
		}
		if count != 0 {
			t.Errorf("Expected 0 reviews (batch rolled back due to FK failure), got %d", count)
		}
	})
}

func TestIntegration_BatchInsertResponses(t *testing.T) {
	pool := openTestPgPool(t)
	ctx := t.Context()

	repoID := createTestRepo(t, pool.Pool(), TestRepoOpts{Identity: "https://github.com/test/batch-responses-test.git"})
	commitID := createTestCommit(t, pool.Pool(), TestCommitOpts{RepoID: repoID, SHA: "batch-responses-sha"})
	jobUUID := uuid.NewString()

	createTestJob(t, pool.pool, TestJobOpts{
		UUID:            jobUUID,
		RepoID:          repoID,
		CommitID:        commitID,
		SourceMachineID: defaultTestMachineID,
	})

	responses := []SyncableResponse{
		{
			UUID:            uuid.NewString(),
			JobUUID:         jobUUID,
			Responder:       "user1",
			Response:        "response 1",
			SourceMachineID: defaultTestMachineID,
			CreatedAt:       time.Now(),
		},
		{
			UUID:            uuid.NewString(),
			JobUUID:         jobUUID,
			Responder:       "user2",
			Response:        "response 2",
			SourceMachineID: defaultTestMachineID,
			CreatedAt:       time.Now(),
		},
		{
			UUID:            uuid.NewString(),
			JobUUID:         jobUUID,
			Responder:       "agent",
			Response:        "response 3",
			SourceMachineID: defaultTestMachineID,
			CreatedAt:       time.Now(),
		},
	}

	success, err := pool.BatchInsertResponses(ctx, responses)
	if err != nil {
		t.Fatalf("BatchInsertResponses failed: %v", err)
	}
	if got := countSuccesses(success); got != 3 {
		t.Errorf("Expected 3 responses inserted, got %d", got)
	}

	t.Run("empty batch is no-op", func(t *testing.T) {
		success, err := pool.BatchInsertResponses(ctx, []SyncableResponse{})
		if err != nil {
			t.Errorf("BatchInsertResponses with empty slice failed: %v", err)
		}
		if success != nil {
			t.Errorf("Expected nil for empty batch, got %v", success)
		}
	})

	t.Run("partial failure with invalid FK", func(t *testing.T) {
		responses := []SyncableResponse{
			{
				UUID:            uuid.NewString(),
				JobUUID:         jobUUID, // Valid FK
				Responder:       "user",
				Response:        "valid response",
				SourceMachineID: defaultTestMachineID,
				CreatedAt:       time.Now(),
			},
			{
				UUID:            uuid.NewString(),
				JobUUID:         "00000000-0000-0000-0000-000000000000", // Invalid FK
				Responder:       "user",
				Response:        "invalid response",
				SourceMachineID: defaultTestMachineID,
				CreatedAt:       time.Now(),
			},
		}

		success, err := pool.BatchInsertResponses(ctx, responses)

		if err == nil {
			t.Error("Expected error from batch with invalid FK, got nil")
		}
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
	ctx := t.Context()

	env := NewMigrationTestEnv(t)
	// Clean up after test
	env.CleanupDropSchema("roborev")

	// Drop any existing schema to start fresh - this test needs to verify v1→v2 migration
	env.DropSchema("roborev")

	// Load and execute v1 schema from embedded SQL file
	// Use helper to parse and execute statements
	for _, stmt := range parseSQLStatements(postgresV1Schema) {
		env.Exec(stmt)
	}

	// Insert a test job to verify data survives migration
	testJobUUID := uuid.NewString()
	var repoID int64
	err := env.QueryRow(`
		INSERT INTO roborev.repos (identity) VALUES ('test-repo-v1-migration') RETURNING id
	`).Scan(&repoID)
	if err != nil {
		t.Fatalf("Failed to insert test repo: %v", err)
	}

	env.Exec(`
		INSERT INTO roborev.review_jobs (uuid, repo_id, git_ref, agent, status, source_machine_id, enqueued_at)
		VALUES ($1, $2, 'HEAD', 'test-agent', 'done', '00000000-0000-0000-0000-000000000001', NOW())
	`, testJobUUID, repoID)

	// Now connect with the normal pool and run EnsureSchema - should migrate v1→v2
	connString := getTestPostgresURL(t)
	pool, err := NewPgPool(ctx, connString, DefaultPgPoolConfig())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	if err := pool.EnsureSchema(ctx); err != nil {
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
	pool := openTestPgPool(t)
	ctx := t.Context()

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
