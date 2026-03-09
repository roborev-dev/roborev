package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// verdictToBool converts a ParseVerdict result ("P"/"F") to an integer
// for storage in the verdict_bool column (1=pass, 0=fail).
func verdictToBool(verdict string) int {
	if verdict == "P" {
		return 1
	}
	return 0
}

// verdictFromBoolOrParse returns the verdict string from a stored verdict_bool
// value. If the value is NULL (legacy row), falls back to ParseVerdict(output).
func verdictFromBoolOrParse(vb sql.NullInt64, output string) string {
	if vb.Valid {
		if vb.Int64 == 1 {
			return "P"
		}
		return "F"
	}
	return ParseVerdict(output)
}

// ParseVerdict extracts P (pass) or F (fail) from review output.
// It intentionally uses a small set of deterministic signals:
// clear severity/findings markers mean fail, and clear pass phrases mean pass.
// We do not try to interpret narrative caveats after "No issues found." because
// that quickly turns into a brittle natural-language parser. If agent output is
// too chatty or mixes process narration with findings, that should be fixed in
// the review prompt rather than by adding more verdict heuristics here.
func ParseVerdict(output string) string {
	// First check for severity labels which indicate actual findings
	// These appear as "- Medium —", "* Low:", "Critical -", etc.
	if hasSeverityLabel(output) {
		return "F"
	}

	for line := range strings.SplitSeq(output, "\n") {
		normalized := normalizeVerdictLine(line)
		if !hasPassPrefix(normalized) {
			continue
		}
		return "P"
	}
	return "F"
}

func normalizeVerdictLine(line string) string {
	normalized := strings.TrimSpace(strings.ToLower(line))
	// Normalize curly apostrophes to straight apostrophes (LLMs sometimes use these)
	normalized = strings.ReplaceAll(normalized, "\u2018", "'") // left single quote
	normalized = strings.ReplaceAll(normalized, "\u2019", "'") // right single quote
	normalized = stripMarkdown(normalized)
	normalized = stripListMarker(normalized)
	return stripFieldLabel(normalized)
}

func hasPassPrefix(line string) bool {
	passPrefixes := []string{
		"no issues",
		"no findings",
		"i didn't find any issues",
		"i did not find any issues",
		"i found no issues",
	}
	for _, prefix := range passPrefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// stripMarkdown removes common markdown formatting from a line
func stripMarkdown(s string) string {
	// Strip leading markdown headers (##, ###, etc.)
	for strings.HasPrefix(s, "#") {
		s = strings.TrimPrefix(s, "#")
	}
	s = strings.TrimSpace(s)

	// Strip bold/italic markers (**, __, *, _)
	// Handle ** and __ first (bold), then * and _ (italic)
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	// Don't strip single * or _ as they might be intentional (e.g., bullet points handled separately)

	return strings.TrimSpace(s)
}

// stripListMarker removes leading bullet/number markers from a line
func stripListMarker(s string) string {
	// Handle: "- ", "* ", "1. ", "99) ", "100. ", etc.
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return s
	}
	// Check for bullet markers
	if s[0] == '-' || s[0] == '*' {
		return strings.TrimSpace(s[1:])
	}
	// Check for numbered lists - scan all leading digits
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			continue
		}
		if i > 0 && (s[i] == '.' || s[i] == ')' || s[i] == ':') {
			return strings.TrimSpace(s[i+1:])
		}
		break
	}
	return s
}

// stripFieldLabel removes a known leading field label from structured review output.
// Handles "Review Findings: No issues found." and similar patterns.
func stripFieldLabel(s string) string {
	labels := []string{
		"review findings",
		"findings",
		"review result",
		"result",
		"verdict",
		"review",
	}
	for _, label := range labels {
		if strings.HasPrefix(s, label) {
			rest := s[len(label):]
			if len(rest) > 0 && rest[0] == ':' {
				return strings.TrimSpace(rest[1:])
			}
		}
	}
	return s
}

// hasSeverityLabel checks if the output contains severity labels indicating findings.
// Matches patterns like "- Medium —", "* Low:", "Critical — issue", etc.
// Checks lines that start with bullets/numbers OR directly with severity words.
// Requires separators to be followed by space to avoid "High-level overview".
// Skips lines that appear to be part of a severity legend/rubric.
func hasSeverityLabel(output string) bool {
	lc := strings.ToLower(output)
	severities := []string{"critical", "high", "medium", "low"}
	lines := strings.Split(lc, "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}

		// Check if line starts with bullet/number - if so, strip it
		first := trimmed[0]
		hasBullet := first == '-' || first == '*' || (first >= '0' && first <= '9') ||
			strings.HasPrefix(trimmed, "•")

		checkText := trimmed
		if hasBullet {
			// Strip leading bullets/asterisks/numbers
			checkText = strings.TrimLeft(trimmed, "-*•0123456789.) ")
			checkText = strings.TrimSpace(checkText)
		}

		// Strip markdown formatting (bold, headers) before checking
		checkText = stripMarkdown(checkText)

		// Check if text starts with a severity word
		for _, sev := range severities {
			if !strings.HasPrefix(checkText, sev) {
				continue
			}

			// Check if followed by separator (dash, em-dash, colon, pipe)
			rest := checkText[len(sev):]
			rest = strings.TrimSpace(rest)
			if len(rest) == 0 {
				continue
			}

			// Check for valid separator
			hasValidSep := false
			// Check for em-dash or en-dash (these are unambiguous)
			if strings.HasPrefix(rest, "—") || strings.HasPrefix(rest, "–") {
				hasValidSep = true
			}
			// Check for colon or pipe (unambiguous separators)
			if rest[0] == ':' || rest[0] == '|' {
				hasValidSep = true
			}
			// For hyphen, require space after to avoid "High-level"
			if rest[0] == '-' && len(rest) > 1 && rest[1] == ' ' {
				hasValidSep = true
			}

			if !hasValidSep {
				continue
			}

			// Skip if this looks like a legend/rubric entry
			// Check if previous non-empty line is a legend header
			if isLegendEntry(lines, i) {
				continue
			}

			return true
		}

		// Check for "severity: <level>" pattern (e.g., "**Severity**: High")
		if strings.HasPrefix(checkText, "severity") {
			rest := checkText[len("severity"):]
			rest = strings.TrimSpace(rest)
			hasSep := len(rest) > 0 && (rest[0] == ':' || rest[0] == '|' ||
				strings.HasPrefix(rest, "—") || strings.HasPrefix(rest, "–"))
			// Accept hyphen-minus when followed by space (mirrors the severity-word branch)
			if !hasSep && len(rest) > 1 && rest[0] == '-' && rest[1] == ' ' {
				hasSep = true
			}
			if hasSep {
				// Skip separator and whitespace
				rest = strings.TrimLeft(rest, ":-–—| ")
				rest = strings.TrimSpace(rest)
				for _, sev := range severities {
					if strings.HasPrefix(rest, sev) {
						if !isLegendEntry(lines, i) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// isLegendEntry checks if a line at index i appears to be part of a severity legend/rubric
// by looking at preceding lines for legend indicators. Scans up to 10 lines back,
// skipping empty lines, severity lines, and description lines that may appear
// between legend entries.
func isLegendEntry(lines []string, i int) bool {
	for j := i - 1; j >= 0 && j >= i-10; j-- {
		prev := strings.TrimSpace(lines[j])
		if len(prev) == 0 {
			continue
		}

		// Strip markdown and list markers so bolded headers like
		// "**Severity levels:**" are recognized the same as plain text.
		prev = stripMarkdown(stripListMarker(prev))

		// Check for legend header patterns (ends with ":" and contains indicator word)
		if strings.HasSuffix(prev, ":") || strings.HasSuffix(prev, "：") {
			if strings.Contains(prev, "severity") || strings.Contains(prev, "level") ||
				strings.Contains(prev, "legend") || strings.Contains(prev, "scale") ||
				strings.Contains(prev, "rating") || strings.Contains(prev, "priority") {
				return true
			}
		}
	}
	return false
}

// parseSQLiteTime parses a time string from SQLite which may be in different formats.
// Handles RFC3339 (what we write), SQLite datetime('now') format, and timezone variants.
// Returns zero time for empty strings. Logs a warning for non-empty unrecognized formats
// to surface driver/schema issues instead of silently producing zero times.
func parseSQLiteTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// Try RFC3339 first (what we write for started_at, finished_at)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	// Try SQLite datetime format (from datetime('now'))
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t
	}
	// Try with timezone
	if t, err := time.Parse("2006-01-02T15:04:05Z07:00", s); err == nil {
		return t
	}
	log.Printf("storage: warning: unrecognized time format %q", s)
	return time.Time{}
}

// EnqueueOpts contains options for creating any type of review job.
// The job type is inferred from which fields are set (in priority order):
//   - Prompt != "" → "task" (custom prompt job)
//   - DiffContent != "" → "dirty" (uncommitted changes)
//   - CommitID > 0 → "review" (single commit)
//   - otherwise → "range" (commit range)
type EnqueueOpts struct {
	RepoID       int64
	CommitID     int64  // >0 for single-commit reviews
	GitRef       string // SHA, "start..end" range, or "dirty"
	Branch       string
	SessionID    string
	Agent        string
	Model        string
	Provider     string // e.g. "anthropic", "openai"
	Reasoning    string
	ReviewType   string // e.g. "security" — changes which system prompt is used
	PatchID      string // Stable patch-id for rebase tracking
	DiffContent  string // For dirty reviews (captured at enqueue time)
	Prompt       string // For task jobs (pre-stored prompt)
	OutputPrefix string // Prefix to prepend to review output
	Agentic      bool   // Allow file edits and command execution
	Label        string // Display label in TUI for task jobs (default: "prompt")
	JobType      string // Explicit job type (review/range/dirty/task/compact/fix); inferred if empty
	ParentJobID  int64  // Parent job being fixed (for fix jobs)
}

// EnqueueJob creates a new review job. The job type is inferred from opts.
func (db *DB) EnqueueJob(opts EnqueueOpts) (*ReviewJob, error) {
	reasoning := opts.Reasoning
	if reasoning == "" {
		reasoning = "thorough"
	}

	// Determine job type from fields (use explicit type if provided)
	var jobType string
	if opts.JobType != "" {
		jobType = opts.JobType
	} else {
		switch {
		case opts.Prompt != "":
			jobType = JobTypeTask
		case opts.DiffContent != "":
			jobType = JobTypeDirty
		case opts.CommitID > 0:
			jobType = JobTypeReview
		default:
			jobType = JobTypeRange
		}
	}

	// For task jobs, use Label as git_ref display value
	gitRef := opts.GitRef
	if jobType == JobTypeTask {
		if opts.Label != "" {
			gitRef = opts.Label
		} else if gitRef == "" {
			gitRef = "prompt"
		}
	}

	agenticInt := 0
	if opts.Agentic {
		agenticInt = 1
	}

	uid := GenerateUUID()
	machineID, _ := db.GetMachineID()
	now := time.Now()
	nowStr := now.Format(time.RFC3339)

	// Use NULL for commit_id when not a single-commit review
	var commitIDParam any
	if opts.CommitID > 0 {
		commitIDParam = opts.CommitID
	}

	// Use NULL for parent_job_id when not a fix job
	var parentJobIDParam any
	if opts.ParentJobID > 0 {
		parentJobIDParam = opts.ParentJobID
	}

	result, err := db.Exec(`
		INSERT INTO review_jobs (repo_id, commit_id, git_ref, branch, session_id, agent, model, provider, reasoning,
			status, job_type, review_type, patch_id, diff_content, prompt, agentic, output_prefix,
			parent_job_id, uuid, source_machine_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		opts.RepoID, commitIDParam, gitRef, nullString(opts.Branch), nullString(opts.SessionID),
		opts.Agent, nullString(opts.Model), nullString(opts.Provider), reasoning,
		jobType, opts.ReviewType, nullString(opts.PatchID),
		nullString(opts.DiffContent), nullString(opts.Prompt), agenticInt,
		nullString(opts.OutputPrefix), parentJobIDParam,
		uid, machineID, nowStr)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	job := &ReviewJob{
		ID:              id,
		RepoID:          opts.RepoID,
		GitRef:          gitRef,
		Branch:          opts.Branch,
		SessionID:       opts.SessionID,
		Agent:           opts.Agent,
		Model:           opts.Model,
		Provider:        opts.Provider,
		Reasoning:       reasoning,
		JobType:         jobType,
		ReviewType:      opts.ReviewType,
		PatchID:         opts.PatchID,
		Status:          JobStatusQueued,
		EnqueuedAt:      now,
		Prompt:          opts.Prompt,
		Agentic:         opts.Agentic,
		OutputPrefix:    opts.OutputPrefix,
		UUID:            uid,
		SourceMachineID: machineID,
		UpdatedAt:       &now,
	}
	if opts.ParentJobID > 0 {
		job.ParentJobID = &opts.ParentJobID
	}
	if opts.CommitID > 0 {
		job.CommitID = &opts.CommitID
	}
	if opts.DiffContent != "" {
		job.DiffContent = &opts.DiffContent
	}
	return job, nil
}

// ClaimJob atomically claims the next queued job for a worker
func (db *DB) ClaimJob(workerID string) (*ReviewJob, error) {
	now := time.Now()
	nowStr := now.Format(time.RFC3339)

	// Atomically claim a job by updating it in a single statement
	// This prevents race conditions where two workers select the same job
	result, err := db.Exec(`
		UPDATE review_jobs
		SET status = 'running', worker_id = ?, started_at = ?, updated_at = ?
		WHERE id = (
			SELECT id FROM review_jobs
			WHERE status = 'queued'
			ORDER BY enqueued_at, id
			LIMIT 1
		)
	`, workerID, nowStr, nowStr)
	if err != nil {
		return nil, err
	}

	// Check if we claimed anything
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rowsAffected == 0 {
		return nil, nil // No jobs available
	}

	// Now fetch the job we just claimed
	var job ReviewJob
	var enqueuedAt string
	var commitID sql.NullInt64
	var commitSubject sql.NullString
	var diffContent sql.NullString
	var prompt sql.NullString
	var model, provider, branch, sessionID sql.NullString
	var agenticInt int
	var jobType sql.NullString
	var reviewType sql.NullString
	var outputPrefix sql.NullString
	var patchID sql.NullString
	var parentJobID sql.NullInt64
	err = db.QueryRow(`
		SELECT j.id, j.repo_id, j.commit_id, j.git_ref, j.branch, j.session_id, j.agent, j.model, j.provider, j.reasoning, j.status, j.enqueued_at,
		       r.root_path, r.name, c.subject, j.diff_content, j.prompt, COALESCE(j.agentic, 0), j.job_type, j.review_type,
		       j.output_prefix, j.patch_id, j.parent_job_id
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		WHERE j.worker_id = ? AND j.status = 'running'
		ORDER BY j.started_at DESC
		LIMIT 1
	`, workerID).Scan(&job.ID, &job.RepoID, &commitID, &job.GitRef, &branch, &sessionID, &job.Agent, &model, &provider, &job.Reasoning, &job.Status, &enqueuedAt,
		&job.RepoPath, &job.RepoName, &commitSubject, &diffContent, &prompt, &agenticInt, &jobType, &reviewType,
		&outputPrefix, &patchID, &parentJobID)
	if err != nil {
		return nil, err
	}

	if commitID.Valid {
		job.CommitID = &commitID.Int64
	}
	if commitSubject.Valid {
		job.CommitSubject = commitSubject.String
	}
	if diffContent.Valid {
		job.DiffContent = &diffContent.String
	}
	if prompt.Valid {
		job.Prompt = prompt.String
	}
	if model.Valid {
		job.Model = model.String
	}
	if provider.Valid {
		job.Provider = provider.String
	}
	if branch.Valid {
		job.Branch = branch.String
	}
	if sessionID.Valid {
		job.SessionID = sessionID.String
	}
	job.Agentic = agenticInt != 0
	if jobType.Valid {
		job.JobType = jobType.String
	}
	if reviewType.Valid {
		job.ReviewType = reviewType.String
	}
	if outputPrefix.Valid {
		job.OutputPrefix = outputPrefix.String
	}
	if patchID.Valid {
		job.PatchID = patchID.String
	}
	if parentJobID.Valid {
		job.ParentJobID = &parentJobID.Int64
	}
	job.EnqueuedAt = parseSQLiteTime(enqueuedAt)
	job.Status = JobStatusRunning
	job.WorkerID = workerID
	job.StartedAt = &now
	return &job, nil
}

// SaveJobPrompt stores the prompt for a running job
func (db *DB) SaveJobPrompt(jobID int64, prompt string) error {
	_, err := db.Exec(`UPDATE review_jobs SET prompt = ? WHERE id = ?`, prompt, jobID)
	return err
}

// SaveJobSessionID stores the captured agent session ID for a job.
// The first captured ID wins so repeated lifecycle events do not
// overwrite it. The update is scoped to the current execution
// attempt: it requires status='running' and the given workerID so
// that a stale worker unwinding after cancel/retry cannot overwrite
// a session ID that belongs to a new attempt of the same job.
func (db *DB) SaveJobSessionID(
	jobID int64, workerID, sessionID string,
) error {
	if sessionID == "" {
		return nil
	}
	now := time.Now().Format(time.RFC3339)
	_, err := db.Exec(`
		UPDATE review_jobs
		SET session_id = ?, updated_at = ?
		WHERE id = ?
		  AND status = 'running'
		  AND worker_id = ?
		  AND (session_id IS NULL OR session_id = '')
	`, sessionID, now, jobID, workerID)
	return err
}

// SaveJobPatch stores the generated patch for a completed fix job
func (db *DB) SaveJobPatch(jobID int64, patch string) error {
	_, err := db.Exec(`UPDATE review_jobs SET patch = ? WHERE id = ?`, patch, jobID)
	return err
}

// CompleteFixJob atomically marks a fix job as done, stores the review,
// and persists the patch in a single transaction. This prevents invalid
// states where a patch is written but the job isn't done, or vice versa.
func (db *DB) CompleteFixJob(jobID int64, agent, prompt, output, patch string) error {
	now := time.Now().Format(time.RFC3339)
	machineID, _ := db.GetMachineID()
	reviewUUID := GenerateUUID()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
				log.Printf("jobs CompleteFixJob: rollback failed: %v", err)
			}
		}
	}()

	// Fetch output_prefix from job (if any)
	var outputPrefix sql.NullString
	err = conn.QueryRowContext(ctx, `SELECT output_prefix FROM review_jobs WHERE id = ?`, jobID).Scan(&outputPrefix)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	finalOutput := output
	if outputPrefix.Valid && outputPrefix.String != "" {
		finalOutput = outputPrefix.String + output
	}

	// Atomically set status=done AND patch in one UPDATE
	result, err := conn.ExecContext(ctx,
		`UPDATE review_jobs SET status = 'done', finished_at = ?, updated_at = ?, patch = ? WHERE id = ? AND status = 'running'`,
		now, now, patch, jobID)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return nil // Job was canceled
	}

	verdictBool := verdictToBool(ParseVerdict(finalOutput))
	_, err = conn.ExecContext(ctx,
		`INSERT INTO reviews (job_id, agent, prompt, output, verdict_bool, uuid, updated_by_machine_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		jobID, agent, prompt, finalOutput, verdictBool, reviewUUID, machineID, now)
	if err != nil {
		return err
	}

	_, err = conn.ExecContext(ctx, "COMMIT")
	if err != nil {
		return err
	}
	committed = true
	return nil
}

// CompleteJob marks a job as done and stores the review.
// Only updates if job is still in 'running' state (respects cancellation).
// If the job has an output_prefix, it will be prepended to the output.
func (db *DB) CompleteJob(jobID int64, agent, prompt, output string) error {
	// Get machine ID and generate UUIDs before starting transaction
	// to avoid potential lock conflicts with GetMachineID's writes
	now := time.Now().Format(time.RFC3339)
	machineID, _ := db.GetMachineID()
	reviewUUID := GenerateUUID()

	// Use BEGIN IMMEDIATE to acquire write lock upfront, avoiding deadlocks
	// when concurrent goroutines (workers, sync) try to upgrade from read to write.
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
				log.Printf("jobs CompleteJob: rollback failed: %v", err)
			}
		}
	}()

	// Fetch output_prefix from job (if any)
	var outputPrefix sql.NullString
	err = conn.QueryRowContext(ctx, `SELECT output_prefix FROM review_jobs WHERE id = ?`, jobID).Scan(&outputPrefix)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	// Prepend output_prefix if present
	finalOutput := output
	if outputPrefix.Valid && outputPrefix.String != "" {
		finalOutput = outputPrefix.String + output
	}

	// Update job status only if still running (not canceled)
	result, err := conn.ExecContext(ctx, `UPDATE review_jobs SET status = 'done', finished_at = ?, updated_at = ? WHERE id = ? AND status = 'running'`, now, now, jobID)
	if err != nil {
		return err
	}

	// Check if we actually updated (job wasn't canceled)
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		// Job was canceled or in unexpected state, don't store review
		return nil
	}

	// Insert review with sync columns
	verdictBool := verdictToBool(ParseVerdict(finalOutput))
	_, err = conn.ExecContext(ctx, `INSERT INTO reviews (job_id, agent, prompt, output, verdict_bool, uuid, updated_by_machine_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		jobID, agent, prompt, finalOutput, verdictBool, reviewUUID, machineID, now)
	if err != nil {
		return err
	}

	_, err = conn.ExecContext(ctx, "COMMIT")
	if err != nil {
		return err
	}
	committed = true
	return nil
}

// FailJob marks a job as failed with an error message.
// Only updates if job is still in 'running' state and owned by the given worker
// (respects cancellation and prevents stale workers from failing reclaimed jobs).
// Pass empty workerID to skip the ownership check (for admin/test callers).
// Returns true if the job was actually updated (false when ownership or status
// check prevented the update).
func (db *DB) FailJob(jobID int64, workerID string, errorMsg string) (bool, error) {
	now := time.Now().Format(time.RFC3339)
	var result sql.Result
	var err error
	if workerID != "" {
		result, err = db.Exec(`UPDATE review_jobs SET status = 'failed', finished_at = ?, error = ?, updated_at = ? WHERE id = ? AND status = 'running' AND worker_id = ?`,
			now, errorMsg, now, jobID, workerID)
	} else {
		result, err = db.Exec(`UPDATE review_jobs SET status = 'failed', finished_at = ?, error = ?, updated_at = ? WHERE id = ? AND status = 'running'`,
			now, errorMsg, now, jobID)
	}
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// CancelJob marks a running or queued job as canceled
func (db *DB) CancelJob(jobID int64) error {
	now := time.Now().Format(time.RFC3339)
	result, err := db.Exec(`
		UPDATE review_jobs
		SET status = 'canceled', finished_at = ?, updated_at = ?
		WHERE id = ? AND status IN ('queued', 'running')
	`, now, now, jobID)
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

// MarkJobApplied transitions a fix job from done to applied.
func (db *DB) MarkJobApplied(jobID int64) error {
	now := time.Now().Format(time.RFC3339)
	result, err := db.Exec(`
		UPDATE review_jobs
		SET status = 'applied', updated_at = ?
		WHERE id = ? AND status = 'done' AND job_type = 'fix'
	`, now, jobID)
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

// MarkJobRebased transitions a done fix job to the "rebased" terminal state.
// This indicates the patch was stale and a new rebase job was triggered.
func (db *DB) MarkJobRebased(jobID int64) error {
	now := time.Now().Format(time.RFC3339)
	result, err := db.Exec(`
		UPDATE review_jobs
		SET status = 'rebased', updated_at = ?
		WHERE id = ? AND status = 'done' AND job_type = 'fix'
	`, now, jobID)
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

// ReenqueueJob resets a completed, failed, or canceled job back to queued status.
// This allows manual re-running of jobs to get a fresh review.
// For done jobs, the existing review is deleted to avoid unique constraint violations.
func (db *DB) ReenqueueJob(jobID int64) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
				log.Printf("jobs ReenqueueJob: rollback failed: %v", err)
			}
		}
	}()

	// Delete any existing review for this job (for done jobs being rerun)
	_, err = conn.ExecContext(ctx, `DELETE FROM reviews WHERE job_id = ?`, jobID)
	if err != nil {
		return err
	}

	// Reset job status
	result, err := conn.ExecContext(ctx, `
		UPDATE review_jobs
		SET status = 'queued', worker_id = NULL, started_at = NULL, finished_at = NULL, error = NULL, retry_count = 0, patch = NULL, session_id = NULL
		WHERE id = ? AND status IN ('done', 'failed', 'canceled')
	`, jobID)
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

	_, err = conn.ExecContext(ctx, "COMMIT")
	if err != nil {
		return err
	}
	committed = true
	return nil
}

// RetryJob requeues a running job for retry if retry_count < maxRetries.
// When workerID is non-empty the update is scoped to the owning worker,
// preventing a stale/zombie worker from requeuing a reclaimed job.
// Pass empty workerID to skip the ownership check (for admin/test callers).
func (db *DB) RetryJob(jobID int64, workerID string, maxRetries int) (bool, error) {
	var result sql.Result
	var err error
	if workerID != "" {
		result, err = db.Exec(`
			UPDATE review_jobs
			SET status = 'queued', worker_id = NULL, started_at = NULL, finished_at = NULL, error = NULL, retry_count = retry_count + 1, session_id = NULL
			WHERE id = ? AND retry_count < ? AND status = 'running' AND worker_id = ?
		`, jobID, maxRetries, workerID)
	} else {
		result, err = db.Exec(`
			UPDATE review_jobs
			SET status = 'queued', worker_id = NULL, started_at = NULL, finished_at = NULL, error = NULL, retry_count = retry_count + 1, session_id = NULL
			WHERE id = ? AND retry_count < ? AND status = 'running'
		`, jobID, maxRetries)
	}
	if err != nil {
		return false, err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	return rows > 0, nil
}

// FailoverJob atomically switches a running job to the given backup agent
// and requeues it. When backupModel is non-empty the job's model is set
// to that value; otherwise model is cleared (NULL) so the backup agent
// uses its CLI default. Returns false if the job is not in running state,
// the worker doesn't own the job, or the backup agent is the same as the
// current agent.
func (db *DB) FailoverJob(jobID int64, workerID, backupAgent, backupModel string) (bool, error) {
	if backupAgent == "" {
		return false, nil
	}
	result, err := db.Exec(`
		UPDATE review_jobs
		SET agent = ?,
		    model = ?,
		    retry_count = 0,
		    status = 'queued',
		    worker_id = NULL,
		    started_at = NULL,
		    finished_at = NULL,
		    error = NULL,
		    session_id = NULL
		WHERE id = ?
		  AND status = 'running'
		  AND worker_id = ?
		  AND agent != ?
	`, backupAgent, nullString(backupModel), jobID, workerID, backupAgent)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

// GetJobRetryCount returns the retry count for a job
func (db *DB) GetJobRetryCount(jobID int64) (int, error) {
	var count int
	err := db.QueryRow(`SELECT retry_count FROM review_jobs WHERE id = ?`, jobID).Scan(&count)
	return count, err
}

// ListJobsOption configures optional filters for ListJobs.
type ListJobsOption func(*listJobsOptions)

type listJobsOptions struct {
	gitRef             string
	branch             string
	branchIncludeEmpty bool
	closed             *bool
	jobType            string
	excludeJobType     string
	repoPrefix         string
}

// WithGitRef filters jobs by git ref.
func WithGitRef(ref string) ListJobsOption {
	return func(o *listJobsOptions) { o.gitRef = ref }
}

// WithBranch filters jobs by exact branch name.
func WithBranch(branch string) ListJobsOption {
	return func(o *listJobsOptions) { o.branch = branch }
}

// WithBranchOrEmpty filters jobs by branch name, also including jobs
// with no branch set (empty string or NULL).
func WithBranchOrEmpty(branch string) ListJobsOption {
	return func(o *listJobsOptions) {
		o.branch = branch
		o.branchIncludeEmpty = true
	}
}

// WithClosed filters jobs by closed state (true/false).
func WithClosed(closed bool) ListJobsOption {
	return func(o *listJobsOptions) { o.closed = &closed }
}

// WithJobType filters jobs by job_type (e.g. "fix", "review").
func WithJobType(jobType string) ListJobsOption {
	return func(o *listJobsOptions) { o.jobType = jobType }
}

// WithExcludeJobType excludes jobs of the given type.
func WithExcludeJobType(jobType string) ListJobsOption {
	return func(o *listJobsOptions) { o.excludeJobType = jobType }
}

// WithRepoPrefix filters jobs to repos whose root_path starts with the given prefix.
func WithRepoPrefix(prefix string) ListJobsOption {
	return func(o *listJobsOptions) {
		// Trim trailing slash so LIKE "prefix/%"  doesn't become "prefix//%".
		// Root prefix "/" trims to "" which disables the filter (all repos match).
		o.repoPrefix = escapeLike(strings.TrimRight(prefix, "/"))
	}
}

// escapeLike escapes SQL LIKE wildcards (% and _) in a literal string.
// Uses '!' as the ESCAPE character to avoid conflicts with backslashes
// in Windows paths stored in root_path.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, "!", "!!")
	s = strings.ReplaceAll(s, "%", "!%")
	s = strings.ReplaceAll(s, "_", "!_")
	return s
}

// ListJobs returns jobs with optional status, repo, branch, and closed filters.
func (db *DB) ListJobs(statusFilter string, repoFilter string, limit, offset int, opts ...ListJobsOption) ([]ReviewJob, error) {
	query := `
		SELECT j.id, j.repo_id, j.commit_id, j.git_ref, j.branch, j.session_id, j.agent, j.reasoning, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error, j.prompt, j.retry_count,
		       COALESCE(j.agentic, 0), r.root_path, r.name, c.subject, rv.closed, rv.output,
		       j.source_machine_id, j.uuid, j.model, j.job_type, j.review_type, j.patch_id,
		       j.parent_job_id, j.provider
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		LEFT JOIN reviews rv ON rv.job_id = j.id
	`
	var args []any
	var conditions []string

	if statusFilter != "" {
		conditions = append(conditions, "j.status = ?")
		args = append(args, statusFilter)
	}
	if repoFilter != "" {
		conditions = append(conditions, "r.root_path = ?")
		args = append(args, repoFilter)
	}
	var o listJobsOptions
	for _, opt := range opts {
		opt(&o)
	}
	if repoFilter == "" && o.repoPrefix != "" {
		conditions = append(conditions, "r.root_path LIKE ? || '/%' ESCAPE '!'")
		args = append(args, o.repoPrefix)
	}
	if o.gitRef != "" {
		conditions = append(conditions, "j.git_ref = ?")
		args = append(args, o.gitRef)
	}
	if o.branch != "" {
		if o.branchIncludeEmpty {
			conditions = append(conditions, "(j.branch = ? OR j.branch = '' OR j.branch IS NULL)")
		} else {
			conditions = append(conditions, "j.branch = ?")
		}
		args = append(args, o.branch)
	}
	if o.closed != nil {
		if *o.closed {
			conditions = append(conditions, "rv.closed = 1")
		} else {
			conditions = append(conditions, "(rv.closed IS NULL OR rv.closed = 0)")
		}
	}
	if o.jobType != "" {
		conditions = append(conditions, "j.job_type = ?")
		args = append(args, o.jobType)
	}
	if o.excludeJobType != "" {
		conditions = append(conditions, "j.job_type != ?")
		args = append(args, o.excludeJobType)
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	query += " ORDER BY j.id DESC"

	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
		// OFFSET requires LIMIT in SQLite
		if offset > 0 {
			query += " OFFSET ?"
			args = append(args, offset)
		}
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []ReviewJob
	for rows.Next() {
		var j ReviewJob
		var enqueuedAt string
		var startedAt, finishedAt, workerID, errMsg, prompt, output, sourceMachineID, jobUUID, model, branch, sessionID, jobTypeStr, reviewTypeStr, patchIDStr, provider sql.NullString
		var commitID sql.NullInt64
		var commitSubject sql.NullString
		var closed sql.NullInt64
		var agentic int
		var parentJobID sql.NullInt64

		err := rows.Scan(&j.ID, &j.RepoID, &commitID, &j.GitRef, &branch, &sessionID, &j.Agent, &j.Reasoning, &j.Status, &enqueuedAt,
			&startedAt, &finishedAt, &workerID, &errMsg, &prompt, &j.RetryCount,
			&agentic, &j.RepoPath, &j.RepoName, &commitSubject, &closed, &output,
			&sourceMachineID, &jobUUID, &model, &jobTypeStr, &reviewTypeStr, &patchIDStr,
			&parentJobID, &provider)
		if err != nil {
			return nil, err
		}

		if jobUUID.Valid {
			j.UUID = jobUUID.String
		}
		if commitID.Valid {
			j.CommitID = &commitID.Int64
		}
		if commitSubject.Valid {
			j.CommitSubject = commitSubject.String
		}
		j.Agentic = agentic != 0
		j.EnqueuedAt = parseSQLiteTime(enqueuedAt)
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
		if prompt.Valid {
			j.Prompt = prompt.String
		}
		if sourceMachineID.Valid {
			j.SourceMachineID = sourceMachineID.String
		}
		if model.Valid {
			j.Model = model.String
		}
		if sessionID.Valid {
			j.SessionID = sessionID.String
		}
		if provider.Valid {
			j.Provider = provider.String
		}
		if jobTypeStr.Valid {
			j.JobType = jobTypeStr.String
		}
		if reviewTypeStr.Valid {
			j.ReviewType = reviewTypeStr.String
		}
		if patchIDStr.Valid {
			j.PatchID = patchIDStr.String
		}
		if branch.Valid {
			j.Branch = branch.String
		}
		if closed.Valid {
			val := closed.Int64 != 0
			j.Closed = &val
		}
		if parentJobID.Valid {
			j.ParentJobID = &parentJobID.Int64
		}
		// Compute verdict only for non-task jobs (task jobs don't have PASS/FAIL verdicts)
		// Task jobs (run, analyze, custom) are identified by having no commit_id and not being dirty
		if output.Valid && !j.IsTaskJob() {
			verdict := ParseVerdict(output.String)
			j.Verdict = &verdict
		}

		jobs = append(jobs, j)
	}

	return jobs, rows.Err()
}

// GetJobByID returns a job by ID with joined fields
// JobStats holds aggregate counts for the queue status line.
type JobStats struct {
	Done   int `json:"done"`
	Closed int `json:"closed"`
	Open   int `json:"open"`
}

// CountJobStats returns aggregate done/closed/open counts
// using the same filter logic as ListJobs (repo, branch, closed).
func (db *DB) CountJobStats(repoFilter string, opts ...ListJobsOption) (JobStats, error) {
	query := `
		SELECT
			COALESCE(SUM(CASE WHEN j.status = 'done' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN j.status = 'done' AND rv.closed = 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN j.status = 'done' AND (rv.closed IS NULL OR rv.closed = 0) THEN 1 ELSE 0 END), 0)
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN reviews rv ON rv.job_id = j.id
	`
	var args []any
	var conditions []string

	if repoFilter != "" {
		conditions = append(conditions, "r.root_path = ?")
		args = append(args, repoFilter)
	}
	var o listJobsOptions
	for _, opt := range opts {
		opt(&o)
	}
	if repoFilter == "" && o.repoPrefix != "" {
		conditions = append(conditions, "r.root_path LIKE ? || '/%' ESCAPE '!'")
		args = append(args, o.repoPrefix)
	}
	if o.branch != "" {
		if o.branchIncludeEmpty {
			conditions = append(conditions, "(j.branch = ? OR j.branch = '' OR j.branch IS NULL)")
		} else {
			conditions = append(conditions, "j.branch = ?")
		}
		args = append(args, o.branch)
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	var stats JobStats
	err := db.QueryRow(query, args...).Scan(&stats.Done, &stats.Closed, &stats.Open)
	return stats, err
}

func (db *DB) GetJobByID(id int64) (*ReviewJob, error) {
	var j ReviewJob
	var enqueuedAt string
	var startedAt, finishedAt, workerID, errMsg, prompt, sessionID sql.NullString
	var commitID sql.NullInt64
	var commitSubject sql.NullString
	var agentic int
	var parentJobID sql.NullInt64
	var patch sql.NullString

	var model, provider, branch, jobTypeStr, reviewTypeStr, patchIDStr sql.NullString
	err := db.QueryRow(`
		SELECT j.id, j.repo_id, j.commit_id, j.git_ref, j.branch, j.session_id, j.agent, j.reasoning, j.status, j.enqueued_at,
		       j.started_at, j.finished_at, j.worker_id, j.error, j.prompt, COALESCE(j.agentic, 0),
		       r.root_path, r.name, c.subject, j.model, j.provider, j.job_type, j.review_type, j.patch_id,
		       j.parent_job_id, j.patch
		FROM review_jobs j
		JOIN repos r ON r.id = j.repo_id
		LEFT JOIN commits c ON c.id = j.commit_id
		WHERE j.id = ?
	`, id).Scan(&j.ID, &j.RepoID, &commitID, &j.GitRef, &branch, &sessionID, &j.Agent, &j.Reasoning, &j.Status, &enqueuedAt,
		&startedAt, &finishedAt, &workerID, &errMsg, &prompt, &agentic,
		&j.RepoPath, &j.RepoName, &commitSubject, &model, &provider, &jobTypeStr, &reviewTypeStr, &patchIDStr,
		&parentJobID, &patch)
	if err != nil {
		return nil, err
	}

	if commitID.Valid {
		j.CommitID = &commitID.Int64
	}
	if commitSubject.Valid {
		j.CommitSubject = commitSubject.String
	}
	j.Agentic = agentic != 0
	j.EnqueuedAt = parseSQLiteTime(enqueuedAt)
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
	if prompt.Valid {
		j.Prompt = prompt.String
	}
	if sessionID.Valid {
		j.SessionID = sessionID.String
	}
	if model.Valid {
		j.Model = model.String
	}
	if provider.Valid {
		j.Provider = provider.String
	}
	if jobTypeStr.Valid {
		j.JobType = jobTypeStr.String
	}
	if reviewTypeStr.Valid {
		j.ReviewType = reviewTypeStr.String
	}
	if patchIDStr.Valid {
		j.PatchID = patchIDStr.String
	}
	if branch.Valid {
		j.Branch = branch.String
	}
	if parentJobID.Valid {
		j.ParentJobID = &parentJobID.Int64
	}
	if patch.Valid {
		j.Patch = &patch.String
	}

	return &j, nil
}

// GetJobCounts returns counts of jobs by status
func (db *DB) GetJobCounts() (queued, running, done, failed, canceled, applied, rebased int, err error) {
	rows, err := db.Query(`SELECT status, COUNT(*) FROM review_jobs GROUP BY status`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int
		if err = rows.Scan(&status, &count); err != nil {
			return
		}
		switch JobStatus(status) {
		case JobStatusQueued:
			queued = count
		case JobStatusRunning:
			running = count
		case JobStatusDone:
			done = count
		case JobStatusFailed:
			failed = count
		case JobStatusCanceled:
			canceled = count
		case JobStatusApplied:
			applied = count
		case JobStatusRebased:
			rebased = count
		}
	}
	err = rows.Err()
	return
}

// UpdateJobBranch sets the branch field for a job that doesn't have one.
// This is used to backfill the branch when it's derived from git.
// Only updates if the current branch is NULL or empty.
// Returns the number of rows affected (0 if branch was already set or job not found, 1 if updated).
func (db *DB) UpdateJobBranch(jobID int64, branch string) (int64, error) {
	result, err := db.Exec(`UPDATE review_jobs SET branch = ? WHERE id = ? AND (branch IS NULL OR branch = '')`, branch, jobID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// RemapJobGitRef updates git_ref and commit_id for jobs matching
// oldSHA in a repo, used after rebases to preserve review history.
// If a job has a stored patch_id that differs from the provided one,
// that job is skipped (the commit's content changed).
// Returns the number of rows updated.
func (db *DB) RemapJobGitRef(
	repoID int64, oldSHA, newSHA, patchID string, newCommitID int64,
) (int, error) {
	now := time.Now().Format(time.RFC3339)
	result, err := db.Exec(`
		UPDATE review_jobs
		SET git_ref = ?, commit_id = ?, patch_id = ?, updated_at = ?
		WHERE git_ref = ? AND repo_id = ?
		AND status != 'running'
		AND (patch_id IS NULL OR patch_id = '' OR patch_id = ?)
	`, newSHA, newCommitID, nullString(patchID), now, oldSHA, repoID, patchID)
	if err != nil {
		return 0, fmt.Errorf("remap job git_ref: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// RemapJob atomically checks for matching jobs, creates the commit
// row, and updates git_ref — all in a single transaction to prevent
// orphan commit rows or races between concurrent remaps.
func (db *DB) RemapJob(
	repoID int64, oldSHA, newSHA, patchID string,
	author, subject string, timestamp time.Time,
) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin remap tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var matchCount int
	err = tx.QueryRow(`
		SELECT COUNT(*) FROM review_jobs
		WHERE git_ref = ? AND repo_id = ?
		AND status != 'running'
		AND (patch_id IS NULL OR patch_id = '' OR patch_id = ?)
	`, oldSHA, repoID, patchID).Scan(&matchCount)
	if err != nil {
		return 0, fmt.Errorf("count matching jobs: %w", err)
	}
	if matchCount == 0 {
		return 0, nil
	}

	// Create or find commit row for the new SHA
	var commitID int64
	err = tx.QueryRow(
		`SELECT id FROM commits WHERE repo_id = ? AND sha = ?`,
		repoID, newSHA,
	).Scan(&commitID)
	if err == sql.ErrNoRows {
		result, insertErr := tx.Exec(`
			INSERT INTO commits (repo_id, sha, author, subject, timestamp)
			VALUES (?, ?, ?, ?, ?)
		`, repoID, newSHA, author, subject,
			timestamp.Format(time.RFC3339))
		if insertErr != nil {
			return 0, fmt.Errorf("create commit: %w", insertErr)
		}
		commitID, _ = result.LastInsertId()
	} else if err != nil {
		return 0, fmt.Errorf("find commit: %w", err)
	}

	now := time.Now().Format(time.RFC3339)
	result, err := tx.Exec(`
		UPDATE review_jobs
		SET git_ref = ?, commit_id = ?, patch_id = ?, updated_at = ?
		WHERE git_ref = ? AND repo_id = ?
		AND status != 'running'
		AND (patch_id IS NULL OR patch_id = '' OR patch_id = ?)
	`, newSHA, commitID, nullString(patchID), now,
		oldSHA, repoID, patchID)
	if err != nil {
		return 0, fmt.Errorf("remap job git_ref: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit remap tx: %w", err)
	}
	return int(n), nil
}
