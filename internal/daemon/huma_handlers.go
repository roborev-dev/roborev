package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/version"
)

// limitNotProvided is the sentinel default for the Limit
// query parameter. A distinct value is needed so that an
// explicit limit=-1 (which legacy clients use to mean
// "unlimited") is distinguishable from "parameter omitted".
const limitNotProvided = -999999

func (s *Server) humaListJobs(
	ctx context.Context, input *ListJobsInput,
) (*ListJobsOutput, error) {
	// Single job lookup by ID (>= 0 because ID=0 should
	// return empty, not fall through to the list path).
	if input.ID >= 0 {
		job, err := s.db.GetJobByID(input.ID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				resp := &ListJobsOutput{}
				resp.Body.Jobs = []storage.ReviewJob{}
				return resp, nil
			}
			return nil, huma.Error500InternalServerError(
				fmt.Sprintf("database error: %v", err),
			)
		}
		job.Patch = nil
		resp := &ListJobsOutput{}
		resp.Body.Jobs = []storage.ReviewJob{*job}
		return resp, nil
	}

	repo := input.Repo
	if repo != "" {
		repo = filepath.ToSlash(filepath.Clean(repo))
	}
	repoPrefix := input.RepoPrefix
	if repoPrefix != "" {
		repoPrefix = filepath.ToSlash(filepath.Clean(repoPrefix))
	}

	const maxLimit = 10000
	limit := 50
	switch {
	case input.Limit == limitNotProvided:
		// Not provided — use default
	case input.Limit < 0:
		limit = 0 // any negative → unlimited (legacy behavior)
	default:
		limit = input.Limit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	offset := max(input.Offset, 0)
	if limit == 0 {
		offset = 0
	}

	fetchLimit := limit
	if limit > 0 {
		fetchLimit = limit + 1
	}

	var listOpts []storage.ListJobsOption
	if input.GitRef != "" {
		listOpts = append(
			listOpts, storage.WithGitRef(input.GitRef),
		)
	}
	if input.Branch != "" {
		if input.BranchIncludeEmpty == "true" {
			listOpts = append(
				listOpts,
				storage.WithBranchOrEmpty(input.Branch),
			)
		} else {
			listOpts = append(
				listOpts, storage.WithBranch(input.Branch),
			)
		}
	}
	if input.Closed == "true" || input.Closed == "false" {
		listOpts = append(
			listOpts,
			storage.WithClosed(input.Closed == "true"),
		)
	}
	if input.JobType != "" {
		listOpts = append(
			listOpts, storage.WithJobType(input.JobType),
		)
	}
	if input.ExcludeJobType != "" {
		listOpts = append(
			listOpts,
			storage.WithExcludeJobType(input.ExcludeJobType),
		)
	}
	if repoPrefix != "" && repo == "" {
		listOpts = append(
			listOpts, storage.WithRepoPrefix(repoPrefix),
		)
	}
	if input.Before > 0 {
		listOpts = append(
			listOpts, storage.WithBeforeCursor(input.Before),
		)
	}

	jobs, err := s.db.ListJobs(
		input.Status, repo, fetchLimit, offset, listOpts...,
	)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			fmt.Sprintf("list jobs: %v", err),
		)
	}

	hasMore := false
	if limit > 0 && len(jobs) > limit {
		hasMore = true
		jobs = jobs[:limit]
	}

	// Stats use same repo/branch filters but ignore closed
	// and pagination.
	var statsOpts []storage.ListJobsOption
	if input.Branch != "" {
		if input.BranchIncludeEmpty == "true" {
			statsOpts = append(
				statsOpts,
				storage.WithBranchOrEmpty(input.Branch),
			)
		} else {
			statsOpts = append(
				statsOpts,
				storage.WithBranch(input.Branch),
			)
		}
	}
	if repoPrefix != "" && repo == "" {
		statsOpts = append(
			statsOpts, storage.WithRepoPrefix(repoPrefix),
		)
	}
	stats, statsErr := s.db.CountJobStats(repo, statsOpts...)
	if statsErr != nil {
		log.Printf(
			"Warning: failed to count job stats: %v", statsErr,
		)
	}

	resp := &ListJobsOutput{}
	resp.Body.Jobs = jobs
	resp.Body.HasMore = hasMore
	resp.Body.Stats = &stats
	return resp, nil
}

func (s *Server) humaGetReview(
	ctx context.Context, input *GetReviewInput,
) (*GetReviewOutput, error) {
	var review *storage.Review
	var err error

	if input.JobID >= 0 {
		review, err = s.db.GetReviewByJobID(input.JobID)
	} else if input.SHA != "" {
		review, err = s.db.GetReviewByCommitSHA(input.SHA)
	} else {
		return nil, huma.Error400BadRequest(
			"job_id or sha parameter required",
		)
	}

	if err != nil {
		return nil, huma.Error404NotFound("review not found")
	}

	return &GetReviewOutput{Body: review}, nil
}

func (s *Server) humaListComments(
	ctx context.Context, input *ListCommentsInput,
) (*ListCommentsOutput, error) {
	var responses []storage.Response
	var err error

	if input.JobID >= 0 {
		responses, err = s.db.GetCommentsForJob(input.JobID)
		if err != nil {
			return nil, huma.Error500InternalServerError(
				fmt.Sprintf("get responses: %v", err),
			)
		}
	} else if input.CommitID >= 0 {
		responses, err = s.db.GetCommentsForCommit(input.CommitID)
		if err != nil {
			return nil, huma.Error500InternalServerError(
				fmt.Sprintf("get responses: %v", err),
			)
		}
	} else if input.SHA != "" {
		responses, err = s.db.GetCommentsForCommitSHA(input.SHA)
		if err != nil {
			return nil, huma.Error404NotFound("commit not found")
		}
	} else {
		return nil, huma.Error400BadRequest(
			"job_id, commit_id, or sha parameter required",
		)
	}

	resp := &ListCommentsOutput{}
	resp.Body.Responses = responses
	return resp, nil
}

func (s *Server) humaListRepos(
	ctx context.Context, input *ListReposInput,
) (*ListReposOutput, error) {
	prefix := input.Prefix
	if prefix != "" {
		prefix = filepath.ToSlash(filepath.Clean(prefix))
	}

	var repoOpts []storage.ListReposOption
	if prefix != "" {
		repoOpts = append(
			repoOpts, storage.WithRepoPathPrefix(prefix),
		)
	}
	if input.Branch != "" {
		repoOpts = append(
			repoOpts, storage.WithRepoBranch(input.Branch),
		)
	}

	repos, totalCount, err := s.db.ListReposWithReviewCounts(
		repoOpts...,
	)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			fmt.Sprintf("list repos: %v", err),
		)
	}

	resp := &ListReposOutput{}
	resp.Body.Repos = repos
	resp.Body.TotalCount = totalCount
	return resp, nil
}

func (s *Server) humaListBranches(
	ctx context.Context, input *ListBranchesInput,
) (*ListBranchesOutput, error) {
	// Filter out empty strings to treat ?repo= as no filter
	var repoPaths []string
	for _, p := range input.Repo {
		if p != "" {
			repoPaths = append(
				repoPaths,
				filepath.ToSlash(filepath.Clean(p)),
			)
		}
	}

	result, err := s.db.ListBranchesWithCounts(repoPaths)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			fmt.Sprintf("list branches: %v", err),
		)
	}

	resp := &ListBranchesOutput{}
	resp.Body.Branches = result.Branches
	resp.Body.TotalCount = result.TotalCount
	resp.Body.NullsRemaining = result.NullsRemaining
	return resp, nil
}

func (s *Server) humaGetStatus(
	ctx context.Context, input *GetStatusInput,
) (*GetStatusOutput, error) {
	queued, running, done, failed, canceled,
		applied, rebased, skipped, err := s.db.GetJobCounts()
	if err != nil {
		return nil, huma.Error500InternalServerError(
			fmt.Sprintf("get counts: %v", err),
		)
	}

	configReloadedAt := ""
	if t := s.configWatcher.LastReloadedAt(); !t.IsZero() {
		configReloadedAt = t.Format(time.RFC3339Nano)
	}
	configReloadCounter := s.configWatcher.ReloadCounter()

	resp := &GetStatusOutput{}
	resp.Body = storage.DaemonStatus{
		Version:             version.Version,
		QueuedJobs:          queued,
		RunningJobs:         running,
		CompletedJobs:       done,
		FailedJobs:          failed,
		CanceledJobs:        canceled,
		AppliedJobs:         applied,
		RebasedJobs:         rebased,
		SkippedJobs:         skipped,
		AutoDesign:          s.autoDesignStatusForResponse(),
		ActiveWorkers:       s.workerPool.ActiveWorkers(),
		MaxWorkers:          s.workerPool.MaxWorkers(),
		MachineID:           s.getMachineID(),
		ConfigReloadedAt:    configReloadedAt,
		ConfigReloadCounter: configReloadCounter,
	}
	return resp, nil
}

func (s *Server) humaGetSummary(
	ctx context.Context, input *GetSummaryInput,
) (*GetSummaryOutput, error) {
	since := time.Now().Add(-7 * 24 * time.Hour)
	if input.Since != "" {
		d, err := parseDuration(input.Since)
		if err != nil {
			return nil, huma.Error400BadRequest(
				fmt.Sprintf("invalid since value: %s", input.Since),
			)
		}
		since = time.Now().Add(-d)
	}

	opts := storage.SummaryOptions{
		RepoPath: input.Repo,
		Branch:   input.Branch,
		Since:    since,
		AllRepos: input.All == "true",
	}

	summary, err := s.db.GetSummary(opts)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			fmt.Sprintf("get summary: %v", err),
		)
	}

	return &GetSummaryOutput{Body: summary}, nil
}

func (s *Server) humaCancelJob(
	ctx context.Context, input *CancelJobInput,
) (*CancelJobOutput, error) {
	if input.Body.JobID == 0 {
		return nil, huma.Error400BadRequest(
			"job_id is required",
		)
	}

	if err := s.db.CancelJob(input.Body.JobID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, huma.Error404NotFound(
				"job not found or not cancellable",
			)
		}
		return nil, huma.Error500InternalServerError(
			fmt.Sprintf("cancel job: %v", err),
		)
	}

	s.workerPool.CancelJob(input.Body.JobID)

	resp := &CancelJobOutput{}
	resp.Body.Success = true
	return resp, nil
}

func (s *Server) humaRerunJob(
	ctx context.Context, input *RerunJobInput,
) (*RerunJobOutput, error) {
	if input.Body.JobID == 0 {
		return nil, huma.Error400BadRequest(
			"job_id is required",
		)
	}

	job, err := s.db.GetJobByID(input.Body.JobID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, huma.Error404NotFound(
				"job not found or not rerunnable",
			)
		}
		return nil, huma.Error500InternalServerError(
			fmt.Sprintf("load job: %v", err),
		)
	}

	model, provider, err := resolveRerunModelProvider(
		job, s.configWatcher.Config(),
	)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}

	err = s.db.ReenqueueJob(
		input.Body.JobID,
		storage.ReenqueueOpts{
			Model:    model,
			Provider: provider,
		},
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, huma.Error404NotFound(
				"job not found or not rerunnable",
			)
		}
		return nil, huma.Error500InternalServerError(
			fmt.Sprintf("rerun job: %v", err),
		)
	}

	resp := &RerunJobOutput{}
	resp.Body.Success = true
	return resp, nil
}

func (s *Server) humaCloseReview(
	ctx context.Context, input *CloseReviewInput,
) (*CloseReviewOutput, error) {
	if input.Body.JobID == 0 {
		return nil, huma.Error400BadRequest(
			"job_id is required",
		)
	}

	err := s.db.MarkReviewClosedByJobID(
		input.Body.JobID, input.Body.Closed,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, huma.Error404NotFound(
				"review not found for job",
			)
		}
		return nil, huma.Error500InternalServerError(
			fmt.Sprintf("mark closed: %v", err),
		)
	}

	eventType := "review.closed"
	if !input.Body.Closed {
		eventType = "review.reopened"
	}
	evt := Event{
		Type:  eventType,
		TS:    time.Now(),
		JobID: input.Body.JobID,
	}
	if job, err := s.db.GetJobByID(input.Body.JobID); err == nil {
		evt.Repo = job.RepoPath
		evt.RepoName = job.RepoName
		evt.SHA = job.GitRef
		evt.Agent = job.Agent
	}
	s.broadcaster.Broadcast(evt)

	resp := &CloseReviewOutput{}
	resp.Body.Success = true
	return resp, nil
}

func (s *Server) humaAddComment(
	ctx context.Context, input *AddCommentInput,
) (*AddCommentOutput, error) {
	if input.Body.Commenter == "" || input.Body.Comment == "" {
		return nil, huma.Error400BadRequest(
			"commenter and comment are required",
		)
	}

	if input.Body.JobID == 0 && input.Body.SHA == "" {
		return nil, huma.Error400BadRequest(
			"job_id or sha is required",
		)
	}

	var resp *storage.Response
	var err error

	if input.Body.JobID != 0 {
		resp, err = s.db.AddCommentToJob(
			input.Body.JobID,
			input.Body.Commenter,
			input.Body.Comment,
		)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, huma.Error404NotFound(
					"job not found",
				)
			}
			return nil, huma.Error500InternalServerError(
				fmt.Sprintf("add comment: %v", err),
			)
		}
	} else {
		commit, commitErr := s.db.GetCommitBySHA(input.Body.SHA)
		if commitErr != nil {
			return nil, huma.Error404NotFound(
				"commit not found",
			)
		}

		resp, err = s.db.AddComment(
			commit.ID,
			input.Body.Commenter,
			input.Body.Comment,
		)
		if err != nil {
			return nil, huma.Error500InternalServerError(
				fmt.Sprintf("add comment: %v", err),
			)
		}
	}

	return &AddCommentOutput{Body: resp}, nil
}
