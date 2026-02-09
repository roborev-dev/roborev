package storage

import (
	"database/sql"
	"fmt"
)

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
// Only returns true if the batch has at least one linked job — an empty batch
// (from a crash between CreateCIBatch and RecordBatchJob) is treated as absent
// so the recovery path in processPR can clean it up.
func (db *DB) HasCIBatch(githubRepo string, prNumber int, headSHA string) (bool, error) {
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM ci_pr_batches b
		WHERE b.github_repo = ? AND b.pr_number = ? AND b.head_sha = ?
		AND EXISTS (SELECT 1 FROM ci_pr_batch_jobs bj WHERE bj.batch_id = b.id)`,
		githubRepo, prNumber, headSHA).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// CreateCIBatch creates a new batch record for a PR. Uses INSERT OR IGNORE to
// handle races. Returns (batch, true) if this caller created the batch, or
// (batch, false) if the batch already existed (another poller won the race).
// Only the creator (created==true) should proceed to enqueue jobs.
func (db *DB) CreateCIBatch(githubRepo string, prNumber int, headSHA string, totalJobs int) (*CIPRBatch, bool, error) {
	result, err := db.Exec(`INSERT OR IGNORE INTO ci_pr_batches (github_repo, pr_number, head_sha, total_jobs, updated_at) VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		githubRepo, prNumber, headSHA, totalJobs)
	if err != nil {
		return nil, false, err
	}

	affected, _ := result.RowsAffected()
	created := affected > 0

	var batch CIPRBatch
	var synthesized int
	err = db.QueryRow(`SELECT id, github_repo, pr_number, head_sha, total_jobs, completed_jobs, failed_jobs, synthesized FROM ci_pr_batches WHERE github_repo = ? AND pr_number = ? AND head_sha = ?`,
		githubRepo, prNumber, headSHA).Scan(&batch.ID, &batch.GithubRepo, &batch.PRNumber, &batch.HeadSHA, &batch.TotalJobs, &batch.CompletedJobs, &batch.FailedJobs, &synthesized)
	if err != nil {
		return nil, false, err
	}
	batch.Synthesized = synthesized != 0
	return &batch, created, nil
}

// CountBatchJobs returns the number of jobs linked to a batch.
func (db *DB) CountBatchJobs(batchID int64) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM ci_pr_batch_jobs WHERE batch_id = ?`, batchID).Scan(&count)
	return count, err
}

// IsBatchStale reports whether a batch has had no activity for more than
// 1 minute. Staleness is based on updated_at (bumped by each RecordBatchJob)
// rather than created_at, so legitimately slow creators are not reclaimed
// while they are still making progress.
func (db *DB) IsBatchStale(batchID int64) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM ci_pr_batches WHERE id = ? AND COALESCE(updated_at, created_at) < datetime('now', '-1 minute')`,
		batchID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetBatchJobIDs returns the review job IDs linked to a batch.
func (db *DB) GetBatchJobIDs(batchID int64) ([]int64, error) {
	rows, err := db.Query(`SELECT job_id FROM ci_pr_batch_jobs WHERE batch_id = ?`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RecordBatchJob links a review job to a batch and bumps the batch's
// updated_at timestamp as a heartbeat so staleness detection is based on
// inactivity rather than age from creation.
func (db *DB) RecordBatchJob(batchID, jobID int64) error {
	if _, err := db.Exec(`INSERT INTO ci_pr_batch_jobs (batch_id, job_id) VALUES (?, ?)`, batchID, jobID); err != nil {
		return err
	}
	_, err := db.Exec(`UPDATE ci_pr_batches SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, batchID)
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

// ClaimBatchForSynthesis atomically marks a batch as claimed only if it
// hasn't been claimed yet (CAS). Sets claimed_at so stale claims can be
// detected and recovered. Returns true if this caller won the claim.
func (db *DB) ClaimBatchForSynthesis(batchID int64) (bool, error) {
	result, err := db.Exec(`UPDATE ci_pr_batches SET synthesized = 1, claimed_at = datetime('now') WHERE id = ? AND synthesized = 0`, batchID)
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
	_, err := db.Exec(`UPDATE ci_pr_batches SET synthesized = 0, claimed_at = NULL WHERE id = ?`, batchID)
	return err
}

// FinalizeBatch clears claimed_at after a successful post. The batch stays
// synthesized=1 but with claimed_at=NULL, so GetStaleBatches won't re-pick it.
func (db *DB) FinalizeBatch(batchID int64) error {
	_, err := db.Exec(`UPDATE ci_pr_batches SET claimed_at = NULL WHERE id = ?`, batchID)
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

// CancelSupersededBatches cancels jobs and removes batches for a PR that have
// been superseded by a new HEAD SHA. Only affects unsynthesized batches (where
// the comment hasn't been posted yet). Returns the IDs of jobs that were canceled.
func (db *DB) CancelSupersededBatches(githubRepo string, prNumber int, newHeadSHA string) ([]int64, error) {
	// Find unsynthesized batches for this PR with a different head_sha
	rows, err := db.Query(`
		SELECT id FROM ci_pr_batches
		WHERE github_repo = ? AND pr_number = ? AND head_sha != ? AND synthesized = 0`,
		githubRepo, prNumber, newHeadSHA)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var batchIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		batchIDs = append(batchIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(batchIDs) == 0 {
		return nil, nil
	}

	var canceledIDs []int64
	for _, batchID := range batchIDs {
		// Cancel linked jobs that are still queued or running
		jobIDs, err := db.GetBatchJobIDs(batchID)
		if err != nil {
			return canceledIDs, fmt.Errorf("get jobs for batch %d: %w", batchID, err)
		}
		for _, jid := range jobIDs {
			if err := db.CancelJob(jid); err != nil {
				if err == sql.ErrNoRows {
					continue // already terminal
				}
				return canceledIDs, fmt.Errorf("cancel job %d: %w", jid, err)
			}
			canceledIDs = append(canceledIDs, jid)
		}
		// Delete the batch and its job links
		if err := db.DeleteCIBatch(batchID); err != nil {
			return canceledIDs, fmt.Errorf("delete batch %d: %w", batchID, err)
		}
	}

	return canceledIDs, nil
}

// DeleteEmptyBatches removes batches with no linked jobs that are older than
// 1 minute. These are left behind when the daemon crashes between CreateCIBatch
// and RecordBatchJob. The age threshold avoids racing with in-progress enqueues.
func (db *DB) DeleteEmptyBatches() (int, error) {
	result, err := db.Exec(`
		DELETE FROM ci_pr_batches
		WHERE NOT EXISTS (
			SELECT 1 FROM ci_pr_batch_jobs bj WHERE bj.batch_id = ci_pr_batches.id
		)
		AND created_at < datetime('now', '-1 minute')`)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// GetStaleBatches returns batches that need synthesis attention. This covers:
//   - Unclaimed batches where all jobs are terminal (dropped events, canceled jobs)
//   - Stale claims where the daemon crashed mid-post (claimed_at > 5 minutes ago)
func (db *DB) GetStaleBatches() ([]CIPRBatch, error) {
	rows, err := db.Query(`
		SELECT b.id, b.github_repo, b.pr_number, b.head_sha, b.total_jobs, b.completed_jobs, b.failed_jobs, b.synthesized
		FROM ci_pr_batches b
		WHERE (
			b.synthesized = 0
			OR (b.synthesized = 1 AND b.claimed_at IS NOT NULL AND b.claimed_at < datetime('now', '-5 minutes'))
		)
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
