package storage

import (
	"database/sql"
	"time"
)

// GetReviewByJobID finds a review by its job ID
func (db *DB) GetReviewByJobID(jobID int64) (*Review, error) {
	var r Review
	var createdAt string
	var addressed int
	var job ReviewJob
	var enqueuedAt string
	var startedAt, finishedAt, workerID, errMsg, reviewUUID sql.NullString
	var commitID sql.NullInt64
	var commitSubject sql.NullString

	err := db.QueryRow(`
		SELECT rv.id, rv.job_id, rv.agent, rv.prompt, rv.output, rv.created_at, rv.addressed, rv.uuid,
		       j.id, j.repo_id, j.commit_id, j.git_ref, j.agent, j.reasoning, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error,
		       rp.root_path, rp.name, c.subject
		FROM reviews rv
		JOIN review_jobs j ON j.id = rv.job_id
		JOIN repos rp ON rp.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		WHERE rv.job_id = ?
	`, jobID).Scan(&r.ID, &r.JobID, &r.Agent, &r.Prompt, &r.Output, &createdAt, &addressed, &reviewUUID,
		&job.ID, &job.RepoID, &commitID, &job.GitRef, &job.Agent, &job.Reasoning, &job.Status, &enqueuedAt,
		&startedAt, &finishedAt, &workerID, &errMsg,
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

	// Compute verdict from review output (only if output exists, no error, and not a prompt job)
	// Prompt jobs are identified by having no commit_id (NULL) - this distinguishes them from
	// regular reviews of branches/commits that might be named "prompt"
	isPromptJob := job.CommitID == nil && job.GitRef == "prompt"
	if r.Output != "" && job.Error == "" && !isPromptJob {
		verdict := ParseVerdict(r.Output)
		job.Verdict = &verdict
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
	var startedAt, finishedAt, workerID, errMsg, reviewUUID sql.NullString
	var commitID sql.NullInt64
	var commitSubject sql.NullString

	// Search by git_ref which contains the SHA for single commits
	err := db.QueryRow(`
		SELECT rv.id, rv.job_id, rv.agent, rv.prompt, rv.output, rv.created_at, rv.addressed, rv.uuid,
		       j.id, j.repo_id, j.commit_id, j.git_ref, j.agent, j.reasoning, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error,
		       rp.root_path, rp.name, c.subject
		FROM reviews rv
		JOIN review_jobs j ON j.id = rv.job_id
		JOIN repos rp ON rp.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		WHERE j.git_ref = ?
		ORDER BY rv.created_at DESC
		LIMIT 1
	`, sha).Scan(&r.ID, &r.JobID, &r.Agent, &r.Prompt, &r.Output, &createdAt, &addressed, &reviewUUID,
		&job.ID, &job.RepoID, &commitID, &job.GitRef, &job.Agent, &job.Reasoning, &job.Status, &enqueuedAt,
		&startedAt, &finishedAt, &workerID, &errMsg,
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

	// Compute verdict from review output (only if output exists, no error, and not a prompt job)
	// Prompt jobs are identified by having no commit_id (NULL) - this distinguishes them from
	// regular reviews of branches/commits that might be named "prompt"
	isPromptJob := job.CommitID == nil && job.GitRef == "prompt"
	if r.Output != "" && job.Error == "" && !isPromptJob {
		verdict := ParseVerdict(r.Output)
		job.Verdict = &verdict
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

// MarkReviewAddressed marks a review as addressed (or unaddressed)
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
