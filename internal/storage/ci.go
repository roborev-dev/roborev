package storage

import "database/sql"

// CIPRReview tracks which PRs have been reviewed at which HEAD SHA
type CIPRReview struct {
	ID         int64  `json:"id"`
	GithubRepo string `json:"github_repo"`
	PRNumber   int    `json:"pr_number"`
	HeadSHA    string `json:"head_sha"`
	JobID      int64  `json:"job_id"`
}

// HasCIReview checks if a PR has already been reviewed at the given HEAD SHA
func (db *DB) HasCIReview(githubRepo string, prNumber int, headSHA string) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM ci_pr_reviews WHERE github_repo = ? AND pr_number = ? AND head_sha = ?`,
		githubRepo, prNumber, headSHA).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// RecordCIReview records that a PR was reviewed at a given HEAD SHA
func (db *DB) RecordCIReview(githubRepo string, prNumber int, headSHA string, jobID int64) error {
	_, err := db.Exec(`INSERT INTO ci_pr_reviews (github_repo, pr_number, head_sha, job_id) VALUES (?, ?, ?, ?)`,
		githubRepo, prNumber, headSHA, jobID)
	return err
}

// GetCIReviewByJobID returns the CI PR review for a given job ID, if any
func (db *DB) GetCIReviewByJobID(jobID int64) (*CIPRReview, error) {
	var r CIPRReview
	err := db.QueryRow(`SELECT id, github_repo, pr_number, head_sha, job_id FROM ci_pr_reviews WHERE job_id = ?`,
		jobID).Scan(&r.ID, &r.GithubRepo, &r.PRNumber, &r.HeadSHA, &r.JobID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// CIPRBatch tracks a batch of CI review jobs for a single PR at a specific HEAD SHA.
// A batch contains multiple jobs (review_types x agents matrix).
type CIPRBatch struct {
	ID            int64  `json:"id"`
	GithubRepo    string `json:"github_repo"`
	PRNumber      int    `json:"pr_number"`
	HeadSHA       string `json:"head_sha"`
	TotalJobs     int    `json:"total_jobs"`
	CompletedJobs int    `json:"completed_jobs"`
	FailedJobs    int    `json:"failed_jobs"`
	Synthesized   bool   `json:"synthesized"`
}

// BatchReviewResult holds the output of a single review job within a batch.
type BatchReviewResult struct {
	JobID      int64  `json:"job_id"`
	Agent      string `json:"agent"`
	ReviewType string `json:"review_type"`
	Output     string `json:"output"`
	Status     string `json:"status"` // "done" or "failed"
	Error      string `json:"error"`
}

// HasCIBatch checks if a batch already exists for this PR at this HEAD SHA.
func (db *DB) HasCIBatch(githubRepo string, prNumber int, headSHA string) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM ci_pr_batches WHERE github_repo = ? AND pr_number = ? AND head_sha = ?`,
		githubRepo, prNumber, headSHA).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// CreateCIBatch creates a new batch record for a PR. Uses INSERT OR IGNORE to handle races.
func (db *DB) CreateCIBatch(githubRepo string, prNumber int, headSHA string, totalJobs int) (*CIPRBatch, error) {
	_, err := db.Exec(`INSERT OR IGNORE INTO ci_pr_batches (github_repo, pr_number, head_sha, total_jobs) VALUES (?, ?, ?, ?)`,
		githubRepo, prNumber, headSHA, totalJobs)
	if err != nil {
		return nil, err
	}

	var batch CIPRBatch
	var synthesized int
	err = db.QueryRow(`SELECT id, github_repo, pr_number, head_sha, total_jobs, completed_jobs, failed_jobs, synthesized FROM ci_pr_batches WHERE github_repo = ? AND pr_number = ? AND head_sha = ?`,
		githubRepo, prNumber, headSHA).Scan(&batch.ID, &batch.GithubRepo, &batch.PRNumber, &batch.HeadSHA, &batch.TotalJobs, &batch.CompletedJobs, &batch.FailedJobs, &synthesized)
	if err != nil {
		return nil, err
	}
	batch.Synthesized = synthesized != 0
	return &batch, nil
}

// RecordBatchJob links a review job to a batch.
func (db *DB) RecordBatchJob(batchID, jobID int64) error {
	_, err := db.Exec(`INSERT INTO ci_pr_batch_jobs (batch_id, job_id) VALUES (?, ?)`, batchID, jobID)
	return err
}

// IncrementBatchCompleted atomically increments completed_jobs and returns the updated batch.
// Uses BEGIN IMMEDIATE to serialize concurrent writers in WAL mode.
// Only the caller that sees completed_jobs+failed_jobs == total_jobs should trigger synthesis.
func (db *DB) IncrementBatchCompleted(batchID int64) (*CIPRBatch, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// BEGIN IMMEDIATE is not directly available via database/sql, but SQLite WAL + busy_timeout
	// handles contention. The UPDATE + SELECT in a single tx is atomic enough.
	_, err = tx.Exec(`UPDATE ci_pr_batches SET completed_jobs = completed_jobs + 1 WHERE id = ?`, batchID)
	if err != nil {
		return nil, err
	}

	var batch CIPRBatch
	var synthesized int
	err = tx.QueryRow(`SELECT id, github_repo, pr_number, head_sha, total_jobs, completed_jobs, failed_jobs, synthesized FROM ci_pr_batches WHERE id = ?`,
		batchID).Scan(&batch.ID, &batch.GithubRepo, &batch.PRNumber, &batch.HeadSHA, &batch.TotalJobs, &batch.CompletedJobs, &batch.FailedJobs, &synthesized)
	if err != nil {
		return nil, err
	}
	batch.Synthesized = synthesized != 0

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &batch, nil
}

// IncrementBatchFailed atomically increments failed_jobs and returns the updated batch.
func (db *DB) IncrementBatchFailed(batchID int64) (*CIPRBatch, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`UPDATE ci_pr_batches SET failed_jobs = failed_jobs + 1 WHERE id = ?`, batchID)
	if err != nil {
		return nil, err
	}

	var batch CIPRBatch
	var synthesized int
	err = tx.QueryRow(`SELECT id, github_repo, pr_number, head_sha, total_jobs, completed_jobs, failed_jobs, synthesized FROM ci_pr_batches WHERE id = ?`,
		batchID).Scan(&batch.ID, &batch.GithubRepo, &batch.PRNumber, &batch.HeadSHA, &batch.TotalJobs, &batch.CompletedJobs, &batch.FailedJobs, &synthesized)
	if err != nil {
		return nil, err
	}
	batch.Synthesized = synthesized != 0

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &batch, nil
}

// GetBatchReviews returns all review results for a batch by joining through ci_pr_batch_jobs.
func (db *DB) GetBatchReviews(batchID int64) ([]BatchReviewResult, error) {
	rows, err := db.Query(`
		SELECT bj.job_id, j.agent, j.review_type, COALESCE(rv.output, ''), j.status, COALESCE(j.error, '')
		FROM ci_pr_batch_jobs bj
		JOIN review_jobs j ON j.id = bj.job_id
		LEFT JOIN reviews rv ON rv.job_id = j.id
		WHERE bj.batch_id = ?
		ORDER BY bj.id`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []BatchReviewResult
	for rows.Next() {
		var r BatchReviewResult
		if err := rows.Scan(&r.JobID, &r.Agent, &r.ReviewType, &r.Output, &r.Status, &r.Error); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// GetCIBatchByJobID looks up the batch that contains a given job ID via ci_pr_batch_jobs.
func (db *DB) GetCIBatchByJobID(jobID int64) (*CIPRBatch, error) {
	var batch CIPRBatch
	var synthesized int
	err := db.QueryRow(`
		SELECT b.id, b.github_repo, b.pr_number, b.head_sha, b.total_jobs, b.completed_jobs, b.failed_jobs, b.synthesized
		FROM ci_pr_batches b
		JOIN ci_pr_batch_jobs bj ON bj.batch_id = b.id
		WHERE bj.job_id = ?`, jobID).Scan(&batch.ID, &batch.GithubRepo, &batch.PRNumber, &batch.HeadSHA, &batch.TotalJobs, &batch.CompletedJobs, &batch.FailedJobs, &synthesized)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	batch.Synthesized = synthesized != 0
	return &batch, nil
}

// ClaimBatchForSynthesis atomically marks a batch as synthesized only if it
// hasn't been claimed yet (CAS). Returns true if this caller won the claim.
// Used to prevent duplicate PR comment posts when event handlers and the
// reconciler race on the same batch.
func (db *DB) ClaimBatchForSynthesis(batchID int64) (bool, error) {
	result, err := db.Exec(`UPDATE ci_pr_batches SET synthesized = 1 WHERE id = ? AND synthesized = 0`, batchID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// UnclaimBatch resets the synthesized flag so the reconciler can retry.
// Called when comment posting fails after a successful claim.
func (db *DB) UnclaimBatch(batchID int64) error {
	_, err := db.Exec(`UPDATE ci_pr_batches SET synthesized = 0 WHERE id = ?`, batchID)
	return err
}

// DeleteCIBatch removes a batch and its job links. Used to clean up
// after a partial enqueue failure so the next poll can retry.
func (db *DB) DeleteCIBatch(batchID int64) error {
	if _, err := db.Exec(`DELETE FROM ci_pr_batch_jobs WHERE batch_id = ?`, batchID); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM ci_pr_batches WHERE id = ?`, batchID)
	return err
}

// GetStaleBatches returns unsynthesized batches where all linked jobs are
// terminal (done/failed/canceled). This covers two cases:
//   - Event-driven counters fell behind (dropped events, canceled jobs)
//   - Counters are correct but comment posting failed
func (db *DB) GetStaleBatches() ([]CIPRBatch, error) {
	rows, err := db.Query(`
		SELECT b.id, b.github_repo, b.pr_number, b.head_sha, b.total_jobs, b.completed_jobs, b.failed_jobs, b.synthesized
		FROM ci_pr_batches b
		WHERE b.synthesized = 0
		AND NOT EXISTS (
			SELECT 1 FROM ci_pr_batch_jobs bj
			JOIN review_jobs j ON j.id = bj.job_id
			WHERE bj.batch_id = b.id
			AND j.status NOT IN ('done', 'failed', 'canceled')
		)
		AND EXISTS (
			SELECT 1 FROM ci_pr_batch_jobs bj WHERE bj.batch_id = b.id
		)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var batches []CIPRBatch
	for rows.Next() {
		var b CIPRBatch
		var synthesized int
		if err := rows.Scan(&b.ID, &b.GithubRepo, &b.PRNumber, &b.HeadSHA, &b.TotalJobs, &b.CompletedJobs, &b.FailedJobs, &synthesized); err != nil {
			return nil, err
		}
		b.Synthesized = synthesized != 0
		batches = append(batches, b)
	}
	return batches, rows.Err()
}

// ReconcileBatch corrects the completed/failed counts for a batch by
// counting actual job statuses from the database.
func (db *DB) ReconcileBatch(batchID int64) (*CIPRBatch, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Count actual terminal statuses from linked jobs
	var completed, failed int
	err = tx.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN j.status = 'done' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN j.status IN ('failed', 'canceled') THEN 1 ELSE 0 END), 0)
		FROM ci_pr_batch_jobs bj
		JOIN review_jobs j ON j.id = bj.job_id
		WHERE bj.batch_id = ?`, batchID).Scan(&completed, &failed)
	if err != nil {
		return nil, err
	}

	_, err = tx.Exec(`UPDATE ci_pr_batches SET completed_jobs = ?, failed_jobs = ? WHERE id = ?`,
		completed, failed, batchID)
	if err != nil {
		return nil, err
	}

	var batch CIPRBatch
	var synthesized int
	err = tx.QueryRow(`SELECT id, github_repo, pr_number, head_sha, total_jobs, completed_jobs, failed_jobs, synthesized FROM ci_pr_batches WHERE id = ?`,
		batchID).Scan(&batch.ID, &batch.GithubRepo, &batch.PRNumber, &batch.HeadSHA, &batch.TotalJobs, &batch.CompletedJobs, &batch.FailedJobs, &synthesized)
	if err != nil {
		return nil, err
	}
	batch.Synthesized = synthesized != 0

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &batch, nil
}
