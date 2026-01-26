package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// GetOrCreateRepo finds or creates a repo by its root path.
// If identity is provided, it will be stored; otherwise the identity field remains NULL.
func (db *DB) GetOrCreateRepo(rootPath string, identity ...string) (*Repo, error) {
	// Normalize path
	absPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}

	// Extract optional identity
	var repoIdentity string
	if len(identity) > 0 {
		repoIdentity = identity[0]
	}

	// Try to find existing by path
	var repo Repo
	var createdAt string
	var identityNullable sql.NullString
	err = db.QueryRow(`SELECT id, root_path, name, identity, created_at FROM repos WHERE root_path = ?`, absPath).
		Scan(&repo.ID, &repo.RootPath, &repo.Name, &identityNullable, &createdAt)
	if err == nil {
		repo.Identity = identityNullable.String
		repo.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)

		// Update identity if provided and not already set
		if repoIdentity != "" && repo.Identity == "" {
			_, err = db.Exec(`UPDATE repos SET identity = ? WHERE id = ?`, repoIdentity, repo.ID)
			if err != nil {
				return nil, fmt.Errorf("update identity: %w", err)
			}
			repo.Identity = repoIdentity
		}
		return &repo, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	// Create new
	name := filepath.Base(absPath)
	var result sql.Result
	if repoIdentity != "" {
		result, err = db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES (?, ?, ?)`, absPath, name, repoIdentity)
	} else {
		result, err = db.Exec(`INSERT INTO repos (root_path, name) VALUES (?, ?)`, absPath, name)
	}
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	return &Repo{
		ID:        id,
		RootPath:  absPath,
		Name:      name,
		Identity:  repoIdentity,
		CreatedAt: time.Now(),
	}, nil
}

// GetRepoByPath returns a repo by its path
func (db *DB) GetRepoByPath(rootPath string) (*Repo, error) {
	absPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}

	var repo Repo
	var createdAt string
	err = db.QueryRow(`SELECT id, root_path, name, created_at FROM repos WHERE root_path = ?`, absPath).
		Scan(&repo.ID, &repo.RootPath, &repo.Name, &createdAt)
	if err != nil {
		return nil, err
	}
	repo.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return &repo, nil
}

// RepoWithCount represents a repo with its total job count
type RepoWithCount struct {
	Name     string `json:"name"`
	RootPath string `json:"root_path"`
	Count    int    `json:"count"`
}

// ListReposWithReviewCounts returns all repos with their total job counts
func (db *DB) ListReposWithReviewCounts() ([]RepoWithCount, int, error) {
	// Query repos with their job counts (includes queued/running, not just completed reviews)
	rows, err := db.Query(`
		SELECT r.name, r.root_path, COUNT(rj.id) as job_count
		FROM repos r
		LEFT JOIN review_jobs rj ON rj.repo_id = r.id
		GROUP BY r.id, r.name, r.root_path
		ORDER BY r.name
	`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var repos []RepoWithCount
	totalCount := 0
	for rows.Next() {
		var rc RepoWithCount
		if err := rows.Scan(&rc.Name, &rc.RootPath, &rc.Count); err != nil {
			return nil, 0, err
		}
		repos = append(repos, rc)
		totalCount += rc.Count
	}
	return repos, totalCount, rows.Err()
}

// ListReposWithReviewCountsByBranch returns repos filtered by branch with their job counts
// If branch is empty, returns all repos. Use "(none)" to filter for jobs without a branch.
func (db *DB) ListReposWithReviewCountsByBranch(branch string) ([]RepoWithCount, int, error) {
	var rows *sql.Rows
	var err error

	if branch == "" {
		// No filter - return all repos
		return db.ListReposWithReviewCounts()
	}

	// Filter by branch (handle "(none)" as NULL/empty branch)
	branchFilter := branch
	if branch == "(none)" {
		branchFilter = ""
	}

	rows, err = db.Query(`
		SELECT r.name, r.root_path, COUNT(rj.id) as job_count
		FROM repos r
		INNER JOIN review_jobs rj ON rj.repo_id = r.id
		WHERE COALESCE(rj.branch, '') = ?
		GROUP BY r.id, r.name, r.root_path
		HAVING job_count > 0
		ORDER BY r.name
	`, branchFilter)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var repos []RepoWithCount
	totalCount := 0
	for rows.Next() {
		var rc RepoWithCount
		if err := rows.Scan(&rc.Name, &rc.RootPath, &rc.Count); err != nil {
			return nil, 0, err
		}
		repos = append(repos, rc)
		totalCount += rc.Count
	}
	return repos, totalCount, rows.Err()
}

// BranchWithCount represents a branch with its total job count
type BranchWithCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// BranchListResult contains branches with counts and metadata
type BranchListResult struct {
	Branches       []BranchWithCount
	TotalCount     int
	NullsRemaining int // Number of jobs with NULL/empty branch (for backfill tracking)
}

// ListBranchesWithCounts returns all branches with their job counts
// If repoPaths is non-empty, filters to jobs in those repos only
func (db *DB) ListBranchesWithCounts(repoPaths []string) (*BranchListResult, error) {
	var rows *sql.Rows
	var err error

	if len(repoPaths) == 0 {
		// No repo filter - count branches across all repos
		rows, err = db.Query(`
			SELECT COALESCE(NULLIF(branch, ''), '(none)') as branch_name, COUNT(*) as job_count
			FROM review_jobs
			GROUP BY branch_name
			ORDER BY job_count DESC, branch_name
		`)
	} else if len(repoPaths) == 1 {
		// Single repo filter
		rows, err = db.Query(`
			SELECT COALESCE(NULLIF(rj.branch, ''), '(none)') as branch_name, COUNT(*) as job_count
			FROM review_jobs rj
			INNER JOIN repos r ON rj.repo_id = r.id
			WHERE r.root_path = ?
			GROUP BY branch_name
			ORDER BY job_count DESC, branch_name
		`, repoPaths[0])
	} else {
		// Multiple repo paths - build IN clause with placeholders
		placeholders := make([]string, len(repoPaths))
		args := make([]interface{}, len(repoPaths))
		for i, p := range repoPaths {
			placeholders[i] = "?"
			args[i] = p
		}
		query := fmt.Sprintf(`
			SELECT COALESCE(NULLIF(rj.branch, ''), '(none)') as branch_name, COUNT(*) as job_count
			FROM review_jobs rj
			INNER JOIN repos r ON rj.repo_id = r.id
			WHERE r.root_path IN (%s)
			GROUP BY branch_name
			ORDER BY job_count DESC, branch_name
		`, strings.Join(placeholders, ","))
		rows, err = db.Query(query, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := &BranchListResult{}
	for rows.Next() {
		var bc BranchWithCount
		if err := rows.Scan(&bc.Name, &bc.Count); err != nil {
			return nil, err
		}
		result.Branches = append(result.Branches, bc)
		result.TotalCount += bc.Count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Count actual NULL branches (not empty string or "(none)" sentinel)
	if err := db.QueryRow("SELECT COUNT(*) FROM review_jobs WHERE branch IS NULL").Scan(&result.NullsRemaining); err != nil {
		return nil, err
	}

	return result, nil
}

// RenameRepo updates the display name of a repo identified by its path or current name
func (db *DB) RenameRepo(identifier, newName string) (int64, error) {
	// Try to match by root_path first (absolute or relative), then by name
	absPath, _ := filepath.Abs(identifier)

	// Try path match first
	result, err := db.Exec(`UPDATE repos SET name = ? WHERE root_path = ?`, newName, absPath)
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	if affected > 0 {
		return affected, nil
	}

	// Try name match
	result, err = db.Exec(`UPDATE repos SET name = ? WHERE name = ?`, newName, identifier)
	if err != nil {
		return 0, err
	}
	affected, _ = result.RowsAffected()
	return affected, nil
}

// ListRepos returns all repos in the database
func (db *DB) ListRepos() ([]Repo, error) {
	rows, err := db.Query(`SELECT id, root_path, name, created_at FROM repos ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var repos []Repo
	for rows.Next() {
		var r Repo
		var createdAt string
		if err := rows.Scan(&r.ID, &r.RootPath, &r.Name, &createdAt); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

// GetRepoByID returns a repo by its ID
func (db *DB) GetRepoByID(id int64) (*Repo, error) {
	var repo Repo
	var createdAt string
	err := db.QueryRow(`SELECT id, root_path, name, created_at FROM repos WHERE id = ?`, id).
		Scan(&repo.ID, &repo.RootPath, &repo.Name, &createdAt)
	if err != nil {
		return nil, err
	}
	repo.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return &repo, nil
}

// GetRepoByName returns a repo by its display name
func (db *DB) GetRepoByName(name string) (*Repo, error) {
	var repo Repo
	var createdAt string
	err := db.QueryRow(`SELECT id, root_path, name, created_at FROM repos WHERE name = ?`, name).
		Scan(&repo.ID, &repo.RootPath, &repo.Name, &createdAt)
	if err != nil {
		return nil, err
	}
	repo.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return &repo, nil
}

// FindRepo finds a repo by path or name (tries path first, then name)
func (db *DB) FindRepo(identifier string) (*Repo, error) {
	// Try by path first
	absPath, _ := filepath.Abs(identifier)
	repo, err := db.GetRepoByPath(absPath)
	if err == nil {
		return repo, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	// Try by name
	repo, err = db.GetRepoByName(identifier)
	if err != nil {
		return nil, err
	}
	return repo, nil
}

// RepoStats contains statistics for a single repo
type RepoStats struct {
	Repo          *Repo
	TotalJobs     int
	QueuedJobs    int
	RunningJobs   int
	CompletedJobs int
	FailedJobs    int
	PassedReviews int
	FailedReviews int
}

// GetRepoStats returns detailed statistics for a repo
func (db *DB) GetRepoStats(repoID int64) (*RepoStats, error) {
	repo, err := db.GetRepoByID(repoID)
	if err != nil {
		return nil, err
	}

	stats := &RepoStats{Repo: repo}

	// Get job counts by status
	rows, err := db.Query(`
		SELECT status, COUNT(*) FROM review_jobs WHERE repo_id = ? GROUP BY status
	`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		stats.TotalJobs += count
		switch JobStatus(status) {
		case JobStatusQueued:
			stats.QueuedJobs = count
		case JobStatusRunning:
			stats.RunningJobs = count
		case JobStatusDone:
			stats.CompletedJobs = count
		case JobStatusFailed:
			stats.FailedJobs = count
		}
	}

	// Get review verdict counts (P/F from output)
	// Exclude prompt jobs (commit_id IS NULL AND git_ref = 'prompt') from verdict stats
	err = db.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN r.output LIKE '%**Verdict: PASS%' OR r.output LIKE '%Verdict: PASS%' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN r.output LIKE '%**Verdict: FAIL%' OR r.output LIKE '%Verdict: FAIL%' THEN 1 ELSE 0 END), 0)
		FROM reviews r
		JOIN review_jobs rj ON r.job_id = rj.id
		WHERE rj.repo_id = ?
		  AND NOT (rj.commit_id IS NULL AND rj.git_ref = 'prompt')
	`, repoID).Scan(&stats.PassedReviews, &stats.FailedReviews)
	if err != nil {
		return nil, err
	}

	return stats, nil
}

// ErrRepoHasJobs is returned when trying to delete a repo with jobs without cascade
var ErrRepoHasJobs = errors.New("repository has existing jobs; use cascade to delete them")

// DeleteRepo deletes a repo and optionally its associated data
// If cascade is true, also deletes all jobs, reviews, and responses for the repo
// If cascade is false and jobs exist, returns ErrRepoHasJobs
func (db *DB) DeleteRepo(repoID int64, cascade bool) error {
	// Use a dedicated connection with BEGIN IMMEDIATE for proper locking
	// This ensures no job can be enqueued between the count check and delete
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	// BEGIN IMMEDIATE acquires a write lock immediately, preventing races
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}

	// Ensure rollback on error
	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	// Check for existing jobs (within transaction for consistency)
	var jobCount int
	err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_jobs WHERE repo_id = ?`, repoID).Scan(&jobCount)
	if err != nil {
		return err
	}

	if !cascade && jobCount > 0 {
		return ErrRepoHasJobs
	}

	if cascade {
		// Delete in correct order due to foreign keys
		// 1a. Delete responses for jobs in this repo (job_id based)
		_, err := conn.ExecContext(ctx, `
			DELETE FROM responses WHERE job_id IN (
				SELECT id FROM review_jobs WHERE repo_id = ?
			)
		`, repoID)
		if err != nil {
			return err
		}

		// 1b. Delete responses for commits in this repo (legacy commit_id based)
		_, err = conn.ExecContext(ctx, `
			DELETE FROM responses WHERE commit_id IN (
				SELECT id FROM commits WHERE repo_id = ?
			)
		`, repoID)
		if err != nil {
			return err
		}

		// 2. Delete reviews for jobs in this repo
		_, err = conn.ExecContext(ctx, `
			DELETE FROM reviews WHERE job_id IN (
				SELECT id FROM review_jobs WHERE repo_id = ?
			)
		`, repoID)
		if err != nil {
			return err
		}

		// 3. Delete jobs for this repo
		_, err = conn.ExecContext(ctx, `DELETE FROM review_jobs WHERE repo_id = ?`, repoID)
		if err != nil {
			return err
		}

		// 4. Delete commits for this repo
		_, err = conn.ExecContext(ctx, `DELETE FROM commits WHERE repo_id = ?`, repoID)
		if err != nil {
			return err
		}
	}

	// Delete the repo itself
	result, err := conn.ExecContext(ctx, `DELETE FROM repos WHERE id = ?`, repoID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

// MergeRepos moves all jobs and commits from sourceRepoID to targetRepoID, then deletes the source repo
func (db *DB) MergeRepos(sourceRepoID, targetRepoID int64) (int64, error) {
	if sourceRepoID == targetRepoID {
		return 0, nil
	}

	// Use a dedicated connection with BEGIN IMMEDIATE for proper locking
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, err
	}

	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	// Move all commits from source to target
	// Note: commits.sha is UNIQUE, so this will fail if both repos have
	// commits with the same SHA (which shouldn't happen for the same git repo)
	// Commit-based responses (legacy) are tied to commit_id which remains valid
	_, err = conn.ExecContext(ctx, `UPDATE commits SET repo_id = ? WHERE repo_id = ?`, targetRepoID, sourceRepoID)
	if err != nil {
		return 0, err
	}

	// Move all jobs from source to target
	result, err := conn.ExecContext(ctx, `UPDATE review_jobs SET repo_id = ? WHERE repo_id = ?`, targetRepoID, sourceRepoID)
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()

	// Delete the source repo (now empty)
	_, err = conn.ExecContext(ctx, `DELETE FROM repos WHERE id = ?`, sourceRepoID)
	if err != nil {
		return 0, err
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return 0, err
	}
	committed = true

	return affected, nil
}
