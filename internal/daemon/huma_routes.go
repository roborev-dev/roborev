package daemon

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/roborev-dev/roborev/internal/version"
)

// registerHumaAPI creates a Huma API on the given mux and registers
// all typed endpoints. The returned huma.API can be used to serve
// the generated OpenAPI spec.
func (s *Server) registerHumaAPI(mux *http.ServeMux) huma.API {
	cfg := huma.DefaultConfig("roborev", version.Version)
	cfg.DocsPath = ""
	cfg.SchemasPath = ""
	api := humago.New(mux, cfg)

	huma.Get(api, "/api/jobs", s.humaListJobs,
		func(o *huma.Operation) {
			o.OperationID = "list-jobs"
			o.Summary = "List review jobs"
			o.Tags = []string{"jobs"}
		})

	huma.Get(api, "/api/review", s.humaGetReview,
		func(o *huma.Operation) {
			o.OperationID = "get-review"
			o.Summary = "Get a review by job ID or SHA"
			o.Tags = []string{"reviews"}
		})

	// /api/job/output is registered as a plain HandleFunc
	// (not Huma) because its stream=1 mode uses NDJSON
	// streaming which doesn't fit Huma's typed response model.

	huma.Get(api, "/api/comments", s.humaListComments,
		func(o *huma.Operation) {
			o.OperationID = "list-comments"
			o.Summary = "List comments for a job or commit"
			o.Tags = []string{"comments"}
		})

	huma.Get(api, "/api/repos", s.humaListRepos,
		func(o *huma.Operation) {
			o.OperationID = "list-repos"
			o.Summary = "List repos with job counts"
			o.Tags = []string{"repos"}
		})

	huma.Get(api, "/api/branches", s.humaListBranches,
		func(o *huma.Operation) {
			o.OperationID = "list-branches"
			o.Summary = "List branches with job counts"
			o.Tags = []string{"repos"}
		})

	huma.Get(api, "/api/status", s.humaGetStatus,
		func(o *huma.Operation) {
			o.OperationID = "get-status"
			o.Summary = "Get daemon status"
			o.Tags = []string{"daemon"}
		})

	huma.Get(api, "/api/summary", s.humaGetSummary,
		func(o *huma.Operation) {
			o.OperationID = "get-summary"
			o.Summary = "Get review summary statistics"
			o.Tags = []string{"daemon"}
		})

	huma.Post(api, "/api/job/cancel", s.humaCancelJob,
		func(o *huma.Operation) {
			o.OperationID = "cancel-job"
			o.Summary = "Cancel a queued or running job"
			o.Tags = []string{"jobs"}
		})

	huma.Post(api, "/api/job/rerun", s.humaRerunJob,
		func(o *huma.Operation) {
			o.OperationID = "rerun-job"
			o.Summary = "Re-enqueue a completed or failed job"
			o.Tags = []string{"jobs"}
		})

	huma.Post(api, "/api/review/close", s.humaCloseReview,
		func(o *huma.Operation) {
			o.OperationID = "close-review"
			o.Summary = "Close or reopen a review"
			o.Tags = []string{"reviews"}
		})

	huma.Post(api, "/api/comment", s.humaAddComment,
		func(o *huma.Operation) {
			o.OperationID = "add-comment"
			o.Summary = "Add a comment to a job or commit"
			o.Tags = []string{"comments"}
			o.DefaultStatus = 201
		})

	return api
}
