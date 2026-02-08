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
