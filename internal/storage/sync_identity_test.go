package storage

import (
	"path/filepath"
	"regexp"
	"testing"
)

func TestGetOrCreateRepoByIdentity(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	t.Run("creates placeholder repo for local identity", func(t *testing.T) {
		localIdentity := "local:my-local-project"

		// Create repo by identity
		repoID, err := db.GetOrCreateRepoByIdentity(localIdentity)
		if err != nil {
			t.Fatalf("GetOrCreateRepoByIdentity failed: %v", err)
		}
		if repoID == 0 {
			t.Fatal("Expected non-zero repo ID")
		}

		// Verify repo has correct fields
		var rootPath, name, identity string
		err = db.QueryRow(`SELECT root_path, name, identity FROM repos WHERE id = ?`, repoID).Scan(&rootPath, &name, &identity)
		if err != nil {
			t.Fatalf("Query repo failed: %v", err)
		}
		if rootPath != localIdentity {
			t.Errorf("Expected root_path %q (placeholder), got %q", localIdentity, rootPath)
		}
		// Name is extracted from identity
		if name != "my-local-project" {
			t.Errorf("Expected name 'my-local-project' (extracted), got %q", name)
		}
		if identity != localIdentity {
			t.Errorf("Expected identity %q, got %q", localIdentity, identity)
		}
	})

	t.Run("returns same ID on subsequent calls", func(t *testing.T) {
		localIdentity := "local:another-project"

		id1, err := db.GetOrCreateRepoByIdentity(localIdentity)
		if err != nil {
			t.Fatalf("First GetOrCreateRepoByIdentity failed: %v", err)
		}

		id2, err := db.GetOrCreateRepoByIdentity(localIdentity)
		if err != nil {
			t.Fatalf("Second GetOrCreateRepoByIdentity failed: %v", err)
		}

		if id1 != id2 {
			t.Errorf("Expected same repo ID, got %d and %d", id1, id2)
		}
	})

	t.Run("creates different repos for different identities", func(t *testing.T) {
		id1, err := db.GetOrCreateRepoByIdentity("local:project-a")
		if err != nil {
			t.Fatalf("First GetOrCreateRepoByIdentity failed: %v", err)
		}

		id2, err := db.GetOrCreateRepoByIdentity("local:project-b")
		if err != nil {
			t.Fatalf("Second GetOrCreateRepoByIdentity failed: %v", err)
		}

		if id1 == id2 {
			t.Errorf("Expected different repo IDs, both got %d", id1)
		}
	})

	t.Run("works with git URL identities too", func(t *testing.T) {
		gitIdentity := "https://github.com/user/repo.git"

		repoID, err := db.GetOrCreateRepoByIdentity(gitIdentity)
		if err != nil {
			t.Fatalf("GetOrCreateRepoByIdentity failed: %v", err)
		}

		// Verify identity is set correctly and name is extracted
		var identity, name string
		err = db.QueryRow(`SELECT identity, name FROM repos WHERE id = ?`, repoID).Scan(&identity, &name)
		if err != nil {
			t.Fatalf("Query repo failed: %v", err)
		}
		if identity != gitIdentity {
			t.Errorf("Expected identity %q, got %q", gitIdentity, identity)
		}
		if name != "repo" {
			t.Errorf("Expected name 'repo' (extracted from URL), got %q", name)
		}
	})

	t.Run("reuses single local repo when one exists", func(t *testing.T) {
		// When exactly one local repo has the identity, use it directly (no placeholder)
		singleIdentity := "git@github.com:org/single-clone-repo.git"

		// Create one local clone with the identity
		result, err := db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES (?, ?, ?)`,
			"/home/user/single-clone", "single-clone", singleIdentity)
		if err != nil {
			t.Fatalf("Insert single clone failed: %v", err)
		}
		localRepoID, _ := result.LastInsertId()

		// GetOrCreateRepoByIdentity should return the existing local repo, not create a placeholder
		gotID, err := db.GetOrCreateRepoByIdentity(singleIdentity)
		if err != nil {
			t.Fatalf("GetOrCreateRepoByIdentity failed: %v", err)
		}
		if gotID != localRepoID {
			t.Errorf("Expected to reuse local repo ID %d, got %d", localRepoID, gotID)
		}

		// Verify no placeholder was created
		var count int
		err = db.QueryRow(`SELECT COUNT(*) FROM repos WHERE root_path = ?`, singleIdentity).Scan(&count)
		if err != nil {
			t.Fatalf("Count query failed: %v", err)
		}
		if count != 0 {
			t.Errorf("Expected no placeholder to be created, found %d", count)
		}
	})

	t.Run("creates placeholder when multiple local clones exist", func(t *testing.T) {
		// When multiple local clones have the same identity, create a placeholder
		sharedIdentity := "git@github.com:org/shared-repo.git"

		// Create two local clones with the same identity
		_, err := db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES (?, ?, ?)`,
			"/home/user/clone-1", "clone-1", sharedIdentity)
		if err != nil {
			t.Fatalf("Insert clone-1 failed: %v", err)
		}
		_, err = db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES (?, ?, ?)`,
			"/home/user/clone-2", "clone-2", sharedIdentity)
		if err != nil {
			t.Fatalf("Insert clone-2 failed: %v", err)
		}

		// GetOrCreateRepoByIdentity should create a placeholder
		placeholderID, err := db.GetOrCreateRepoByIdentity(sharedIdentity)
		if err != nil {
			t.Fatalf("GetOrCreateRepoByIdentity should succeed with duplicates, got: %v", err)
		}

		// Verify it created a placeholder (root_path == identity)
		var rootPath, name string
		err = db.QueryRow(`SELECT root_path, name FROM repos WHERE id = ?`, placeholderID).Scan(&rootPath, &name)
		if err != nil {
			t.Fatalf("Query placeholder failed: %v", err)
		}
		if rootPath != sharedIdentity {
			t.Errorf("Expected placeholder root_path %q, got %q", sharedIdentity, rootPath)
		}
		if name != "shared-repo" {
			t.Errorf("Expected placeholder name 'shared-repo' (extracted), got %q", name)
		}

		// Subsequent calls should return the same placeholder
		placeholderID2, err := db.GetOrCreateRepoByIdentity(sharedIdentity)
		if err != nil {
			t.Fatalf("Second GetOrCreateRepoByIdentity failed: %v", err)
		}
		if placeholderID != placeholderID2 {
			t.Errorf("Expected same placeholder ID, got %d and %d", placeholderID, placeholderID2)
		}
	})

	t.Run("prefers single local repo over existing placeholder", func(t *testing.T) {
		// This tests the fix for review #2658: when a placeholder exists from
		// a previous sync (e.g., when there were 0 clones), but now there's
		// exactly one local repo, we should prefer the local repo.
		placeholderIdentity := "git@github.com:org/placeholder-then-clone.git"

		// First, create a placeholder (simulates sync with no local clone)
		_, err := db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES (?, ?, ?)`,
			placeholderIdentity, "placeholder-then-clone", placeholderIdentity)
		if err != nil {
			t.Fatalf("Insert placeholder failed: %v", err)
		}

		// Now create a single local clone (user cloned the repo after syncing)
		result, err := db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES (?, ?, ?)`,
			"/home/user/new-clone", "new-clone", placeholderIdentity)
		if err != nil {
			t.Fatalf("Insert local clone failed: %v", err)
		}
		localRepoID, _ := result.LastInsertId()

		// GetOrCreateRepoByIdentity should return the local repo, not the placeholder
		gotID, err := db.GetOrCreateRepoByIdentity(placeholderIdentity)
		if err != nil {
			t.Fatalf("GetOrCreateRepoByIdentity failed: %v", err)
		}
		if gotID != localRepoID {
			t.Errorf("Expected to prefer local repo ID %d over placeholder, got %d", localRepoID, gotID)
		}
	})
}

func TestExtractRepoNameFromIdentity(t *testing.T) {
	tests := []struct {
		identity string
		expected string
	}{
		// SSH format
		{"git@github.com:org/repo.git", "repo"},
		{"git@github.com:user/my-project.git", "my-project"},
		{"git@gitlab.com:group/subgroup/repo.git", "repo"},

		// HTTPS format
		{"https://github.com/org/repo.git", "repo"},
		{"https://github.com/org/repo", "repo"},
		{"https://gitlab.com/group/subgroup/project.git", "project"},

		// Local format
		{"local:my-project", "my-project"},
		{"local:another-repo", "another-repo"},

		// Edge cases
		{"repo.git", "repo"},
		{"repo", "repo"},
		{"", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.identity, func(t *testing.T) {
			got := ExtractRepoNameFromIdentity(tt.identity)
			if got != tt.expected {
				t.Errorf("ExtractRepoNameFromIdentity(%q) = %q, want %q", tt.identity, got, tt.expected)
			}
		})
	}
}

func TestSetRepoIdentity(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create test repo
	repo, err := db.GetOrCreateRepo(t.TempDir())
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Set identity
	err = db.SetRepoIdentity(repo.ID, "https://github.com/user/repo.git")
	if err != nil {
		t.Fatalf("SetRepoIdentity failed: %v", err)
	}

	// Verify via GetRepoByIdentity
	found, err := db.GetRepoByIdentity("https://github.com/user/repo.git")
	if err != nil {
		t.Fatalf("GetRepoByIdentity failed: %v", err)
	}
	if found == nil {
		t.Fatal("Expected to find repo by identity")
	}
	if found.ID != repo.ID {
		t.Errorf("Expected repo ID %d, got %d", repo.ID, found.ID)
	}
	if found.Identity != "https://github.com/user/repo.git" {
		t.Errorf("Expected identity 'https://github.com/user/repo.git', got %q", found.Identity)
	}
}

func TestGetRepoByIdentity_NotFound(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	found, err := db.GetRepoByIdentity("nonexistent")
	if err != nil {
		t.Fatalf("GetRepoByIdentity failed: %v", err)
	}
	if found != nil {
		t.Errorf("Expected nil for nonexistent identity, got %+v", found)
	}
}

func TestGetRepoByIdentity_DuplicateError(t *testing.T) {
	// This test verifies GetRepoByIdentity returns an error if duplicates exist.
	// Multiple repos can share the same identity (e.g., multiple clones of the same remote),
	// but GetRepoByIdentity should return an error when asked to find a unique repo.
	db := openTestDB(t)
	defer db.Close()

	// Create two repos with same identity (simulates two clones of the same remote)
	_, err := db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES ('/path1', 'repo1', 'same-id')`)
	if err != nil {
		t.Fatalf("Failed to insert repo1: %v", err)
	}
	_, err = db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES ('/path2', 'repo2', 'same-id')`)
	if err != nil {
		t.Fatalf("Failed to insert repo2: %v", err)
	}

	// GetRepoByIdentity should return error for duplicates
	_, err = db.GetRepoByIdentity("same-id")
	if err == nil {
		t.Fatal("Expected error for duplicate identities, but got nil")
	}
	if !regexp.MustCompile(`multiple repos found`).MatchString(err.Error()) {
		t.Errorf("Expected 'multiple repos found' error, got: %v", err)
	}
}

func TestCommitsMigration_SameSHADifferentRepos(t *testing.T) {
	// This test creates an old-schema database manually, runs migration,
	// and verifies that the same SHA can now exist in different repos.
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Create database with old schema (sha TEXT UNIQUE NOT NULL)
	rawDB, err := openRawDB(dbPath)
	if err != nil {
		t.Fatalf("Failed to open raw database: %v", err)
	}

	// Create old schema with UNIQUE(sha) constraint
	setupLegacySchema(t, rawDB, legacySchemaV1DDL)

	// Insert two repos
	_, err = rawDB.Exec(`INSERT INTO repos (root_path, name) VALUES ('/repo1', 'repo1')`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to insert repo1: %v", err)
	}
	_, err = rawDB.Exec(`INSERT INTO repos (root_path, name) VALUES ('/repo2', 'repo2')`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to insert repo2: %v", err)
	}

	// Insert a commit in repo1
	_, err = rawDB.Exec(`INSERT INTO commits (repo_id, sha, author, subject, timestamp) VALUES (1, 'abc123', 'Author', 'Subject', '2024-01-01T00:00:00Z')`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to insert commit in repo1: %v", err)
	}

	// Verify old schema prevents same SHA in different repo
	_, err = rawDB.Exec(`INSERT INTO commits (repo_id, sha, author, subject, timestamp) VALUES (2, 'abc123', 'Author', 'Subject', '2024-01-01T00:00:00Z')`)
	if err == nil {
		rawDB.Close()
		t.Fatal("Expected error inserting duplicate SHA in old schema, but got none")
	}

	rawDB.Close()

	// Now open with our storage.Open which runs migrations
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database with migrations: %v", err)
	}
	defer db.Close()

	// After migration, same SHA in different repo should work
	_, err = db.Exec(`INSERT INTO commits (repo_id, sha, author, subject, timestamp) VALUES (2, 'abc123', 'Author', 'Subject', '2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("After migration, same SHA in different repo should succeed: %v", err)
	}

	// Verify both commits exist
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM commits WHERE sha = 'abc123'`).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count commits: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 commits with sha abc123, got %d", count)
	}

	// Verify duplicate in same repo is still rejected
	_, err = db.Exec(`INSERT INTO commits (repo_id, sha, author, subject, timestamp) VALUES (1, 'abc123', 'Author', 'Subject', '2024-01-01T00:00:00Z')`)
	if err == nil {
		t.Error("Expected error inserting duplicate SHA in same repo, but got none")
	}
}

func TestDuplicateRepoIdentity_MigrationSuccess(t *testing.T) {
	// This test verifies that migration succeeds even when duplicate
	// repos.identity values exist. Multiple clones of the same repo
	// should be allowed (fix for https://github.com/roborev-dev/roborev/issues/131).
	dbPath := filepath.Join(t.TempDir(), "test.db")

	rawDB, err := openRawDB(dbPath)
	if err != nil {
		t.Fatalf("Failed to open raw database: %v", err)
	}

	// Create schema with identity column but no index (simulates partial migration)
	setupLegacySchema(t, rawDB, legacySchemaV2DDL)

	// Insert two repos with the same identity (e.g., two clones of same remote)
	_, err = rawDB.Exec(`INSERT INTO repos (root_path, name, identity) VALUES ('/repo1', 'repo1', 'git@github.com:org/repo.git')`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to insert repo1: %v", err)
	}
	_, err = rawDB.Exec(`INSERT INTO repos (root_path, name, identity) VALUES ('/repo2', 'repo2', 'git@github.com:org/repo.git')`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to insert repo2: %v", err)
	}

	rawDB.Close()

	// Now open with storage.Open which runs migrations - should succeed
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Expected migration to succeed with duplicate identities, but got error: %v", err)
	}
	defer db.Close()

	// Verify both repos exist
	repos, err := db.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos failed: %v", err)
	}
	if len(repos) != 2 {
		t.Errorf("Expected 2 repos, got %d", len(repos))
	}
}

func TestUniqueIndexMigration(t *testing.T) {
	// This test verifies that an existing database with the old UNIQUE index
	// on repos.identity is properly migrated to a non-unique index.
	// See: https://github.com/roborev-dev/roborev/issues/131
	dbPath := filepath.Join(t.TempDir(), "test.db")

	rawDB, err := openRawDB(dbPath)
	if err != nil {
		t.Fatalf("Failed to open raw database: %v", err)
	}

	// Create schema with identity column AND the old unique index
	setupLegacySchema(t, rawDB, legacySchemaV3DDL)

	// Insert one repo
	_, err = rawDB.Exec(`INSERT INTO repos (root_path, name, identity) VALUES ('/repo1', 'repo1', 'git@github.com:org/repo.git')`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("Failed to insert repo1: %v", err)
	}

	rawDB.Close()

	// Open with storage.Open which runs migrations
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Migration failed: %v", err)
	}

	// Verify we can now insert a second repo with the same identity
	// (this would fail if the unique index wasn't converted to non-unique)
	_, err = db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES ('/repo2', 'repo2', 'git@github.com:org/repo.git')`)
	if err != nil {
		db.Close()
		t.Fatalf("Inserting second repo with same identity should succeed after migration, but got: %v", err)
	}

	db.Close()
}
