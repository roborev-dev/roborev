package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// GetReviewByJobID finds a review by its job ID
func (db *DB) GetReviewByJobID(jobID int64) (*Review, error) {
	var r Review
	var createdAt string
	var addressed int
	var job ReviewJob
	var enqueuedAt string
	var startedAt, finishedAt, workerID, errMsg, reviewUUID, model, jobTypeStr, reviewTypeStr, patchIDStr sql.NullString
	var commitID sql.NullInt64
	var commitSubject sql.NullString

	var verdictBool sql.NullInt64
	err := db.QueryRow(`
		SELECT rv.id, rv.job_id, rv.agent, rv.prompt, rv.output, rv.created_at, rv.addressed, rv.uuid, rv.verdict_bool,
		       j.id, j.repo_id, j.commit_id, j.git_ref, j.agent, j.reasoning, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error, j.model, j.job_type, j.review_type, j.patch_id,
		       rp.root_path, rp.name, c.subject
		FROM reviews rv
		JOIN review_jobs j ON j.id = rv.job_id
		JOIN repos rp ON rp.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		WHERE rv.job_id = ?
	`, jobID).Scan(&r.ID, &r.JobID, &r.Agent, &r.Prompt, &r.Output, &createdAt, &addressed, &reviewUUID, &verdictBool,
		&job.ID, &job.RepoID, &commitID, &job.GitRef, &job.Agent, &job.Reasoning, &job.Status, &enqueuedAt,
		&startedAt, &finishedAt, &workerID, &errMsg, &model, &jobTypeStr, &reviewTypeStr, &patchIDStr,
		&job.RepoPath, &job.RepoName, &commitSubject)
	if err != nil {
		return nil, err
	}
	r.Addressed = addressed != 0
	if reviewUUID.Valid {
		r.UUID = reviewUUID.String
	}

	r.CreatedAt = parseSQLiteTime(createdAt)
	if commitID.Valid {
		job.CommitID = &commitID.Int64
	}
	if commitSubject.Valid {
		job.CommitSubject = commitSubject.String
	}
	if model.Valid {
		job.Model = model.String
	}
	if jobTypeStr.Valid {
		job.JobType = jobTypeStr.String
	}
	if reviewTypeStr.Valid {
		job.ReviewType = reviewTypeStr.String
	}
	if patchIDStr.Valid {
		job.PatchID = patchIDStr.String
	}
	job.EnqueuedAt = parseSQLiteTime(enqueuedAt)
	if startedAt.Valid {
		t := parseSQLiteTime(startedAt.String)
		job.StartedAt = &t
	}
	if finishedAt.Valid {
		t := parseSQLiteTime(finishedAt.String)
		job.FinishedAt = &t
	}
	if workerID.Valid {
		job.WorkerID = workerID.String
	}
	if errMsg.Valid {
		job.Error = errMsg.String
	}

	// Use stored verdict_bool if available, otherwise fall back to ParseVerdict
	if r.Output != "" && job.Error == "" && !job.IsTaskJob() {
		verdict := verdictFromBoolOrParse(verdictBool, r.Output)
		job.Verdict = &verdict
	}
	if verdictBool.Valid {
		v := int(verdictBool.Int64)
		r.VerdictBool = &v
	}

	r.Job = &job

	return &r, nil
}

// GetReviewByCommitSHA finds the most recent review by commit SHA (searches git_ref field)
func (db *DB) GetReviewByCommitSHA(sha string) (*Review, error) {
	var r Review
	var createdAt string
	var addressed int
	var job ReviewJob
	var enqueuedAt string
	var startedAt, finishedAt, workerID, errMsg, reviewUUID, model, jobTypeStr, reviewTypeStr, patchIDStr sql.NullString
	var commitID sql.NullInt64
	var commitSubject sql.NullString

	// Search by git_ref which contains the SHA for single commits
	var verdictBool sql.NullInt64
	err := db.QueryRow(`
		SELECT rv.id, rv.job_id, rv.agent, rv.prompt, rv.output, rv.created_at, rv.addressed, rv.uuid, rv.verdict_bool,
		       j.id, j.repo_id, j.commit_id, j.git_ref, j.agent, j.reasoning, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error, j.model, j.job_type, j.review_type, j.patch_id,
		       rp.root_path, rp.name, c.subject
		FROM reviews rv
		JOIN review_jobs j ON j.id = rv.job_id
		JOIN repos rp ON rp.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		WHERE j.git_ref = ?
		ORDER BY rv.created_at DESC
		LIMIT 1
	`, sha).Scan(&r.ID, &r.JobID, &r.Agent, &r.Prompt, &r.Output, &createdAt, &addressed, &reviewUUID, &verdictBool,
		&job.ID, &job.RepoID, &commitID, &job.GitRef, &job.Agent, &job.Reasoning, &job.Status, &enqueuedAt,
		&startedAt, &finishedAt, &workerID, &errMsg, &model, &jobTypeStr, &reviewTypeStr, &patchIDStr,
		&job.RepoPath, &job.RepoName, &commitSubject)
	if err != nil {
		return nil, err
	}
	r.Addressed = addressed != 0
	if reviewUUID.Valid {
		r.UUID = reviewUUID.String
	}

	if commitID.Valid {
		job.CommitID = &commitID.Int64
	}
	if commitSubject.Valid {
		job.CommitSubject = commitSubject.String
	}
	if model.Valid {
		job.Model = model.String
	}
	if jobTypeStr.Valid {
		job.JobType = jobTypeStr.String
	}
	if reviewTypeStr.Valid {
		job.ReviewType = reviewTypeStr.String
	}
	if patchIDStr.Valid {
		job.PatchID = patchIDStr.String
	}

	r.CreatedAt = parseSQLiteTime(createdAt)
	job.EnqueuedAt = parseSQLiteTime(enqueuedAt)
	if startedAt.Valid {
		t := parseSQLiteTime(startedAt.String)
		job.StartedAt = &t
	}
	if finishedAt.Valid {
		t := parseSQLiteTime(finishedAt.String)
		job.FinishedAt = &t
	}
	if workerID.Valid {
		job.WorkerID = workerID.String
	}
	if errMsg.Valid {
		job.Error = errMsg.String
	}

	// Use stored verdict_bool if available, otherwise fall back to ParseVerdict
	if r.Output != "" && job.Error == "" && !job.IsTaskJob() {
		verdict := verdictFromBoolOrParse(verdictBool, r.Output)
		job.Verdict = &verdict
	}
	if verdictBool.Valid {
		v := int(verdictBool.Int64)
		r.VerdictBool = &v
	}

	r.Job = &job

	return &r, nil
}

// GetAllReviewsForGitRef returns all reviews for a git ref (commit SHA or range) for re-review context
func (db *DB) GetAllReviewsForGitRef(gitRef string) ([]Review, error) {
	rows, err := db.Query(`
		SELECT rv.id, rv.job_id, rv.agent, rv.prompt, rv.output, rv.created_at, rv.addressed
		FROM reviews rv
		JOIN review_jobs j ON j.id = rv.job_id
		WHERE j.git_ref = ?
		ORDER BY rv.created_at ASC
	`, gitRef)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reviews []Review
	for rows.Next() {
		var r Review
		var createdAt string
		var addressed int
		if err := rows.Scan(&r.ID, &r.JobID, &r.Agent, &r.Prompt, &r.Output, &createdAt, &addressed); err != nil {
			return nil, err
		}
		r.CreatedAt = parseSQLiteTime(createdAt)
		r.Addressed = addressed != 0
		reviews = append(reviews, r)
	}

	return reviews, rows.Err()
}

// GetRecentReviewsForRepo returns the N most recent reviews for a repo
func (db *DB) GetRecentReviewsForRepo(repoID int64, limit int) ([]Review, error) {
	rows, err := db.Query(`
		SELECT rv.id, rv.job_id, rv.agent, rv.prompt, rv.output, rv.created_at, rv.addressed
		FROM reviews rv
		JOIN review_jobs j ON j.id = rv.job_id
		WHERE j.repo_id = ?
		ORDER BY rv.created_at DESC
		LIMIT ?
	`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reviews []Review
	for rows.Next() {
		var r Review
		var createdAt string
		var addressed int
		if err := rows.Scan(&r.ID, &r.JobID, &r.Agent, &r.Prompt, &r.Output, &createdAt, &addressed); err != nil {
			return nil, err
		}
		r.CreatedAt = parseSQLiteTime(createdAt)
		r.Addressed = addressed != 0
		reviews = append(reviews, r)
	}

	return reviews, rows.Err()
}

// MarkReviewAddressed marks a review as addressed (or unaddressed) by review ID
func (db *DB) MarkReviewAddressed(reviewID int64, addressed bool) error {
	val := 0
	if addressed {
		val = 1
	}
	now := time.Now().Format(time.RFC3339)
	machineID, _ := db.GetMachineID()

	result, err := db.Exec(`UPDATE reviews SET addressed = ?, updated_by_machine_id = ?, updated_at = ? WHERE id = ?`, val, machineID, now, reviewID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// MarkReviewAddressedByJobID marks a review as addressed (or unaddressed) by job ID
func (db *DB) MarkReviewAddressedByJobID(jobID int64, addressed bool) error {
	val := 0
	if addressed {
		val = 1
	}
	now := time.Now().Format(time.RFC3339)
	machineID, _ := db.GetMachineID()

	result, err := db.Exec(`UPDATE reviews SET addressed = ?, updated_by_machine_id = ?, updated_at = ? WHERE job_id = ?`, val, machineID, now, jobID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetJobsWithReviewsByIDs fetches jobs and their reviews in batch for the given job IDs.
// Returns a map of job ID to JobWithReview. Jobs without reviews are included with a nil Review.
func (db *DB) GetJobsWithReviewsByIDs(jobIDs []int64) (map[int64]JobWithReview, error) {
	if len(jobIDs) == 0 {
		return nil, nil
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(jobIDs))
	args := make([]any, len(jobIDs))
	for i, id := range jobIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := strings.Join(placeholders, ",")

	// Fetch jobs
	// Note: The IN clause is built dynamically, but this is safe from SQL injection.
	// The `placeholders` slice contains only "?" characters, and the `args` slice
	// contains the integer IDs, which are passed to the DB driver for parameterization.
	// This prevents user-controlled input from being part of the SQL query string itself.
	jobQuery := fmt.Sprintf(`
		SELECT j.id, j.repo_id, j.commit_id, j.git_ref, j.branch, j.agent, j.reasoning, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error, COALESCE(j.agentic, 0),
		       r.root_path, r.name, c.subject, j.model, j.job_type, j.review_type
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		WHERE j.id IN (%s)
	`, inClause)

	rows, err := db.Query(jobQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("query jobs: %w", err)
	}
	defer rows.Close()

	result := make(map[int64]JobWithReview, len(jobIDs))
	for rows.Next() {
		var j ReviewJob
		var enqueuedAt string
		var startedAt, finishedAt, workerID, errMsg sql.NullString
		var commitID sql.NullInt64
		var commitSubject sql.NullString
		var agentic int
		var model, branch, jobTypeStr, reviewTypeStr sql.NullString

		if err := rows.Scan(&j.ID, &j.RepoID, &commitID, &j.GitRef, &branch, &j.Agent, &j.Reasoning, &j.Status, &enqueuedAt,
			&startedAt, &finishedAt, &workerID, &errMsg, &agentic,
			&j.RepoPath, &j.RepoName, &commitSubject, &model, &jobTypeStr, &reviewTypeStr); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}

		j.EnqueuedAt = parseSQLiteTime(enqueuedAt)
		if commitID.Valid {
			j.CommitID = &commitID.Int64
		}
		if commitSubject.Valid {
			j.CommitSubject = commitSubject.String
		}
		if model.Valid {
			j.Model = model.String
		}
		if branch.Valid {
			j.Branch = branch.String
		}
		if jobTypeStr.Valid {
			j.JobType = jobTypeStr.String
		}
		if reviewTypeStr.Valid {
			j.ReviewType = reviewTypeStr.String
		}
		if startedAt.Valid {
			t := parseSQLiteTime(startedAt.String)
			j.StartedAt = &t
		}
		if finishedAt.Valid {
			t := parseSQLiteTime(finishedAt.String)
			j.FinishedAt = &t
		}
		if workerID.Valid {
			j.WorkerID = workerID.String
		}
		if errMsg.Valid {
			j.Error = errMsg.String
		}
		j.Agentic = agentic != 0

		result[j.ID] = JobWithReview{Job: j}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}

	// Fetch reviews for these jobs
	reviewQuery := fmt.Sprintf(`
		SELECT rv.id, rv.job_id, rv.agent, rv.prompt, rv.output, rv.created_at, rv.addressed, rv.verdict_bool
		FROM reviews rv
		WHERE rv.job_id IN (%s)
	`, inClause)

	reviewRows, err := db.Query(reviewQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("query reviews: %w", err)
	}
	defer reviewRows.Close()

	for reviewRows.Next() {
		var r Review
		var createdAt string
		var addressed int
		var verdictBool sql.NullInt64
		if err := reviewRows.Scan(&r.ID, &r.JobID, &r.Agent, &r.Prompt, &r.Output, &createdAt, &addressed, &verdictBool); err != nil {
			return nil, fmt.Errorf("scan review: %w", err)
		}
		r.CreatedAt = parseSQLiteTime(createdAt)
		r.Addressed = addressed != 0
		if verdictBool.Valid {
			v := int(verdictBool.Int64)
			r.VerdictBool = &v
		}

		if entry, ok := result[r.JobID]; ok {
			entry.Review = &r
			// Populate verdict on the job, matching GetReviewByJobID/GetReviewByCommitSHA
			if r.Output != "" && entry.Job.Error == "" && !entry.Job.IsTaskJob() {
				verdict := verdictFromBoolOrParse(verdictBool, r.Output)
				entry.Job.Verdict = &verdict
			}
			result[r.JobID] = entry
		}
	}
	if err := reviewRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reviews: %w", err)
	}

	return result, nil
}

// GetReviewByID finds a review by its ID
func (db *DB) GetReviewByID(reviewID int64) (*Review, error) {
	var r Review
	var createdAt string
	var addressed int

	err := db.QueryRow(`
		SELECT id, job_id, agent, prompt, output, created_at, addressed
		FROM reviews WHERE id = ?
	`, reviewID).Scan(&r.ID, &r.JobID, &r.Agent, &r.Prompt, &r.Output, &createdAt, &addressed)
	if err != nil {
		return nil, err
	}
	r.CreatedAt = parseSQLiteTime(createdAt)
	r.Addressed = addressed != 0

	return &r, nil
}

// AddComment adds a comment to a commit (legacy - use AddCommentToJob for new code)
func (db *DB) AddComment(commitID int64, responder, response string) (*Response, error) {
	uuid := GenerateUUID()
	machineID, _ := db.GetMachineID()
	now := time.Now()
	nowStr := now.Format(time.RFC3339)

	result, err := db.Exec(`INSERT INTO responses (commit_id, responder, response, uuid, source_machine_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		commitID, responder, response, uuid, machineID, nowStr)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	return &Response{
		ID:              id,
		CommitID:        &commitID,
		Responder:       responder,
		Response:        response,
		CreatedAt:       now,
		UUID:            uuid,
		SourceMachineID: machineID,
	}, nil
}

// AddCommentToJob adds a comment linked to a job/review
func (db *DB) AddCommentToJob(jobID int64, responder, response string) (*Response, error) {
	// Verify job exists first to return proper 404 instead of FK violation or orphaned row
	var exists int
	err := db.QueryRow(`SELECT 1 FROM review_jobs WHERE id = ?`, jobID).Scan(&exists)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows // Job not found
		}
		return nil, err
	}

	uuid := GenerateUUID()
	machineID, _ := db.GetMachineID()
	now := time.Now()
	nowStr := now.Format(time.RFC3339)

	result, err := db.Exec(`INSERT INTO responses (job_id, responder, response, uuid, source_machine_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		jobID, responder, response, uuid, machineID, nowStr)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	return &Response{
		ID:              id,
		JobID:           &jobID,
		Responder:       responder,
		Response:        response,
		CreatedAt:       now,
		UUID:            uuid,
		SourceMachineID: machineID,
	}, nil
}

// GetCommentsForCommit returns all comments for a commit
func (db *DB) GetCommentsForCommit(commitID int64) ([]Response, error) {
	rows, err := db.Query(`
		SELECT id, commit_id, job_id, responder, response, created_at
		FROM responses
		WHERE commit_id = ?
		ORDER BY created_at ASC
	`, commitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var responses []Response
	for rows.Next() {
		var r Response
		var createdAt string
		var commitIDNull, jobIDNull sql.NullInt64
		if err := rows.Scan(&r.ID, &commitIDNull, &jobIDNull, &r.Responder, &r.Response, &createdAt); err != nil {
			return nil, err
		}
		if commitIDNull.Valid {
			r.CommitID = &commitIDNull.Int64
		}
		if jobIDNull.Valid {
			r.JobID = &jobIDNull.Int64
		}
		r.CreatedAt = parseSQLiteTime(createdAt)
		responses = append(responses, r)
	}

	return responses, rows.Err()
}

// GetCommentsForJob returns all comments linked to a job
func (db *DB) GetCommentsForJob(jobID int64) ([]Response, error) {
	rows, err := db.Query(`
		SELECT id, commit_id, job_id, responder, response, created_at
		FROM responses
		WHERE job_id = ?
		ORDER BY created_at ASC
	`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var responses []Response
	for rows.Next() {
		var r Response
		var createdAt string
		var commitIDNull, jobIDNull sql.NullInt64
		if err := rows.Scan(&r.ID, &commitIDNull, &jobIDNull, &r.Responder, &r.Response, &createdAt); err != nil {
			return nil, err
		}
		if commitIDNull.Valid {
			r.CommitID = &commitIDNull.Int64
		}
		if jobIDNull.Valid {
			r.JobID = &jobIDNull.Int64
		}
		r.CreatedAt = parseSQLiteTime(createdAt)
		responses = append(responses, r)
	}

	return responses, rows.Err()
}

// GetCommentsForCommitSHA returns all comments for a commit by SHA
func (db *DB) GetCommentsForCommitSHA(sha string) ([]Response, error) {
	commit, err := db.GetCommitBySHA(sha)
	if err != nil {
		return nil, err
	}
	return db.GetCommentsForCommit(commit.ID)
}
