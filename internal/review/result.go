// Package review provides daemon-free review orchestration: parallel
// batch execution, synthesis, and comment formatting.
package review

import "unicode/utf8"

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

// TrimPartialRune removes a trailing incomplete UTF-8 sequence that
// may result from slicing a string at an arbitrary byte offset. Only
// the last rune is inspected — pre-existing invalid bytes elsewhere
// in the string are left untouched.
func TrimPartialRune(s string) string {
	if len(s) == 0 {
		return s
	}
	r, size := utf8.DecodeLastRuneInString(s)
	if r == utf8.RuneError && size == 1 {
		// Walk back past continuation bytes of the broken
		// sequence (at most 3 bytes for a 4-byte rune).
		i := len(s) - 1
		for i > 0 && !utf8.RuneStart(s[i]) {
			i--
		}
		return s[:i]
	}
	return s
}

// QuotaErrorPrefix is prepended to error messages when a review
// fails due to agent quota exhaustion rather than a real error.
// Matches the prefix set by internal/daemon/worker.go.
const QuotaErrorPrefix = "quota: "
