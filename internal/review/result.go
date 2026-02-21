// Package review provides daemon-free review orchestration: parallel
// batch execution, synthesis, and comment formatting.
package review

// ReviewResult holds the outcome of a single review in a batch.
// Decoupled from storage.BatchReviewResult for daemon-free use.
type ReviewResult struct {
	Agent      string
	ReviewType string
	Output     string
	Status     string // ResultDone or ResultFailed
	Error      string
}

// Result status values for ReviewResult.Status.
const (
	ResultDone   = "done"
	ResultFailed = "failed"
)

// MaxCommentLen is the maximum length for a GitHub PR comment.
// GitHub's hard limit is ~65536; we leave headroom.
const MaxCommentLen = 60000

// QuotaErrorPrefix is prepended to error messages when a review
// fails due to agent quota exhaustion rather than a real error.
// Matches the prefix set by internal/daemon/worker.go.
const QuotaErrorPrefix = "quota: "
