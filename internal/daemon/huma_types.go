package daemon

import (
	"github.com/roborev-dev/roborev/internal/storage"
)

// -- GET /api/jobs --

// ListJobsInput holds query parameters for listing jobs.
// Huma does not support pointer types for query parameters,
// so we use sentinel values: ID=0 means not provided,
// Limit=-1 means use the default (50), Before=0 means
// no cursor.
type ListJobsInput struct {
	ID                 int64  `query:"id" default:"0" doc:"Return a single job by ID"`
	Status             string `query:"status" doc:"Filter by job status"`
	Repo               string `query:"repo" doc:"Filter by repo root path"`
	GitRef             string `query:"git_ref" doc:"Filter by git ref"`
	Branch             string `query:"branch" doc:"Filter by branch name"`
	BranchIncludeEmpty string `query:"branch_include_empty" doc:"Include jobs with no branch when filtering by branch" enum:"true,false,"`
	Closed             string `query:"closed" doc:"Filter by review closed state" enum:"true,false,"`
	JobType            string `query:"job_type" doc:"Filter by job type"`
	ExcludeJobType     string `query:"exclude_job_type" doc:"Exclude jobs of this type"`
	RepoPrefix         string `query:"repo_prefix" doc:"Filter repos by path prefix"`
	Limit              int    `query:"limit" default:"-1" doc:"Max results (default 50, 0=unlimited, max 10000)"`
	Offset             int    `query:"offset" default:"-1" doc:"Skip N results (requires limit>0)"`
	Before             int64  `query:"before" default:"0" doc:"Cursor: return jobs with ID < this value"`
}

// ListJobsOutput is the response for GET /api/jobs.
type ListJobsOutput struct {
	Body struct {
		Jobs    []storage.ReviewJob `json:"jobs"`
		HasMore bool                `json:"has_more"`
		Stats   storage.JobStats    `json:"stats"`
	}
}

// -- GET /api/review --

// GetReviewInput holds query parameters for fetching a review.
type GetReviewInput struct {
	JobID int64  `query:"job_id" default:"0" doc:"Look up review by job ID"`
	SHA   string `query:"sha" doc:"Look up review by commit SHA"`
}

// GetReviewOutput is the response for GET /api/review.
type GetReviewOutput struct {
	Body *storage.Review
}

// -- POST /api/job/cancel --

// CancelJobInput is the request body for canceling a job.
type CancelJobInput struct {
	Body CancelJobRequest
}

// CancelJobOutput is the response for POST /api/job/cancel.
type CancelJobOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

// -- POST /api/job/rerun --

// RerunJobInput is the request body for rerunning a job.
type RerunJobInput struct {
	Body RerunJobRequest
}

// RerunJobOutput is the response for POST /api/job/rerun.
type RerunJobOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

// -- GET /api/job/output --

// GetJobOutputInput holds query parameters for fetching job output.
type GetJobOutputInput struct {
	JobID  int64  `query:"job_id" required:"true" doc:"Job ID to fetch output for"`
	Stream string `query:"stream" doc:"Set to 1 for SSE streaming mode"`
}

// GetJobOutputOutput is the polling-mode response
// for GET /api/job/output.
type GetJobOutputOutput struct {
	Body JobOutputResponse
}

// -- POST /api/review/close --

// CloseReviewInput is the request body for closing/reopening a review.
type CloseReviewInput struct {
	Body CloseReviewRequest
}

// CloseReviewOutput is the response for POST /api/review/close.
type CloseReviewOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

// -- POST /api/comment --

// AddCommentInput is the request body for adding a comment.
type AddCommentInput struct {
	Body AddCommentRequest
}

// AddCommentOutput is the response for POST /api/comment.
type AddCommentOutput struct {
	Body *storage.Response
}

// -- GET /api/comments --

// ListCommentsInput holds query parameters for listing comments.
type ListCommentsInput struct {
	JobID    int64  `query:"job_id" default:"0" doc:"List comments by job ID"`
	CommitID int64  `query:"commit_id" default:"0" doc:"List comments by commit ID"`
	SHA      string `query:"sha" doc:"List comments by commit SHA"`
}

// ListCommentsOutput is the response for GET /api/comments.
type ListCommentsOutput struct {
	Body struct {
		Responses []storage.Response `json:"responses"`
	}
}

// -- GET /api/repos --

// ListReposInput holds query parameters for listing repos.
type ListReposInput struct {
	Branch string `query:"branch" doc:"Filter to repos with jobs on this branch"`
	Prefix string `query:"prefix" doc:"Filter repos by path prefix"`
}

// ListReposOutput is the response for GET /api/repos.
type ListReposOutput struct {
	Body struct {
		Repos      []storage.RepoWithCount `json:"repos"`
		TotalCount int                     `json:"total_count"`
	}
}

// -- GET /api/branches --

// ListBranchesInput holds query parameters for listing branches.
type ListBranchesInput struct {
	Repo []string `query:"repo" doc:"Filter to branches in these repo paths"`
}

// ListBranchesOutput is the response for GET /api/branches.
type ListBranchesOutput struct {
	Body struct {
		Branches       []storage.BranchWithCount `json:"branches"`
		TotalCount     int                       `json:"total_count"`
		NullsRemaining int                       `json:"nulls_remaining"`
	}
}

// -- GET /api/status --

// GetStatusInput is an empty input for the status endpoint.
type GetStatusInput struct{}

// GetStatusOutput is the response for GET /api/status.
type GetStatusOutput struct {
	Body storage.DaemonStatus
}

// -- GET /api/summary --

// GetSummaryInput holds query parameters for the summary endpoint.
type GetSummaryInput struct {
	Since  string `query:"since" doc:"Time window (e.g. 7d, 24h). Default: 7d"`
	Repo   string `query:"repo" doc:"Filter by repo root path"`
	Branch string `query:"branch" doc:"Filter by branch name"`
	All    string `query:"all" doc:"Include per-repo breakdown" enum:"true,false"`
}

// GetSummaryOutput is the response for GET /api/summary.
type GetSummaryOutput struct {
	Body *storage.Summary
}
