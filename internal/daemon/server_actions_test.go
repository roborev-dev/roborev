package daemon

import (
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandleStatus(t *testing.T) {
	server, _, _ := newTestServer(t)

	t.Run("returns status with version", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		w := httptest.NewRecorder()

		server.handleStatus(w, req)

		if w.Code != http.StatusOK {
			require.Condition(t, func() bool {
				return false
			}, "Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var status storage.DaemonStatus
		testutil.DecodeJSON(t, w, &status)

		// Version should be set (non-empty)
		if status.Version == "" {
			assert.Condition(t, func() bool {
				return false
			}, "Expected Version to be set in status response")
		}
	})

	t.Run("wrong method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/status", nil)
		w := httptest.NewRecorder()

		server.handleStatus(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 405 for POST, got %d", w.Code)
		}
	})

	t.Run("returns max_workers from pool not config", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		w := httptest.NewRecorder()

		server.handleStatus(w, req)

		var status storage.DaemonStatus
		testutil.DecodeJSON(t, w, &status)

		// MaxWorkers should match the pool size (config default), not a potentially reloaded config value
		expectedWorkers := config.DefaultConfig().MaxWorkers
		if status.MaxWorkers != expectedWorkers {
			assert.Condition(t, func() bool {
				return false
			}, "Expected MaxWorkers %d from pool, got %d", expectedWorkers, status.MaxWorkers)
		}
	})

	t.Run("config_reloaded_at empty initially", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		w := httptest.NewRecorder()

		server.handleStatus(w, req)

		var status storage.DaemonStatus
		testutil.DecodeJSON(t, w, &status)

		// ConfigReloadedAt should be empty when no reload has occurred
		if status.ConfigReloadedAt != "" {
			assert.Condition(t, func() bool {
				return false
			}, "Expected ConfigReloadedAt to be empty initially, got %q", status.ConfigReloadedAt)
		}
	})
}

func TestHandlePing(t *testing.T) {
	server, _, _ := newTestServer(t)

	t.Run("returns daemon identity", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
		w := httptest.NewRecorder()

		server.handlePing(w, req)

		if w.Code != http.StatusOK {
			require.Condition(t, func() bool {
				return false
			}, "Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var ping PingInfo
		testutil.DecodeJSON(t, w, &ping)
		if ping.Service != daemonServiceName {
			require.Condition(t, func() bool {
				return false
			}, "Expected service %q, got %q", daemonServiceName, ping.Service)
		}
		if ping.Version == "" {
			require.Condition(t, func() bool {
				return false
			}, "Expected ping version to be set")
		}
		if ping.PID == 0 {
			require.Condition(t, func() bool {
				return false
			}, "Expected ping PID to be set")
		}
	})

	t.Run("wrong method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/ping", nil)
		w := httptest.NewRecorder()

		server.handlePing(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			require.Condition(t, func() bool {
				return false
			}, "Expected status 405, got %d", w.Code)
		}
	})
}

func TestHandleCancelJob(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create a repo and job
	job := createTestJob(t, db, tmpDir, "canceltest", "test")

	t.Run("cancel queued job", func(t *testing.T) {
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/cancel", CancelJobRequest{JobID: job.ID})
		w := httptest.NewRecorder()

		server.handleCancelJob(w, req)

		if w.Code != http.StatusOK {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		updated, err := db.GetJobByID(job.ID)
		if err != nil {
			require.Condition(t, func() bool {
				return false
			}, "GetJobByID failed: %v", err)
		}
		if updated.Status != storage.JobStatusCanceled {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 'canceled', got '%s'", updated.Status)
		}
	})

	t.Run("cancel already canceled job fails", func(t *testing.T) {
		// Job is already canceled from previous test
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/cancel", CancelJobRequest{JobID: job.ID})
		w := httptest.NewRecorder()

		server.handleCancelJob(w, req)

		if w.Code != http.StatusNotFound {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 404 for already canceled job, got %d", w.Code)
		}
	})

	t.Run("cancel nonexistent job fails", func(t *testing.T) {
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/cancel", CancelJobRequest{JobID: 99999})
		w := httptest.NewRecorder()

		server.handleCancelJob(w, req)

		if w.Code != http.StatusNotFound {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 404 for nonexistent job, got %d", w.Code)
		}
	})

	t.Run("cancel with missing job_id fails", func(t *testing.T) {
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/cancel", map[string]any{})
		w := httptest.NewRecorder()

		server.handleCancelJob(w, req)

		if w.Code != http.StatusBadRequest {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 400 for missing job_id, got %d", w.Code)
		}
	})

	t.Run("cancel with wrong method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/job/cancel", nil)
		w := httptest.NewRecorder()

		server.handleCancelJob(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 405 for GET, got %d", w.Code)
		}
	})

	t.Run("cancel running job", func(t *testing.T) {
		// Create a new job and claim it
		commit2, err := db.GetOrCreateCommit(job.RepoID, "cancelrunning", "Author", "Subject", time.Now())
		if err != nil {
			require.Condition(t, func() bool {
				return false
			}, "GetOrCreateCommit failed: %v", err)
		}
		job2, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: job.RepoID, CommitID: commit2.ID, GitRef: "cancelrunning", Agent: "test"})
		if err != nil {
			require.Condition(t, func() bool {
				return false
			}, "EnqueueJob failed: %v", err)
		}
		if _, err := db.ClaimJob("worker-1"); err != nil {
			require.Condition(t, func() bool {
				return false
			}, "ClaimJob failed: %v", err)
		}

		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/cancel", CancelJobRequest{JobID: job2.ID})
		w := httptest.NewRecorder()

		server.handleCancelJob(w, req)

		if w.Code != http.StatusOK {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		updated, err := db.GetJobByID(job2.ID)
		if err != nil {
			require.Condition(t, func() bool {
				return false
			}, "GetJobByID failed: %v", err)
		}
		if updated.Status != storage.JobStatusCanceled {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 'canceled', got '%s'", updated.Status)
		}
	})
}

func TestHandleRerunJob(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create a repo
	repo, err := db.GetOrCreateRepo(tmpDir)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "GetOrCreateRepo failed: %v", err)
	}

	t.Run("rerun failed job", func(t *testing.T) {
		commit, _ := db.GetOrCreateCommit(repo.ID, "rerun-failed", "Author", "Subject", time.Now())
		job, _ := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rerun-failed", Agent: "test"})
		db.ClaimJob("worker-1")
		db.FailJob(job.ID, "", "some error")

		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/rerun", RerunJobRequest{JobID: job.ID})
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusOK {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		updated, err := db.GetJobByID(job.ID)
		if err != nil {
			require.Condition(t, func() bool {
				return false
			}, "GetJobByID failed: %v", err)
		}
		if updated.Status != storage.JobStatusQueued {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 'queued', got '%s'", updated.Status)
		}
	})

	t.Run("rerun canceled job", func(t *testing.T) {
		commit, _ := db.GetOrCreateCommit(repo.ID, "rerun-canceled", "Author", "Subject", time.Now())
		job, _ := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rerun-canceled", Agent: "test"})
		db.CancelJob(job.ID)

		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/rerun", RerunJobRequest{JobID: job.ID})
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusOK {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		updated, err := db.GetJobByID(job.ID)
		if err != nil {
			require.Condition(t, func() bool {
				return false
			}, "GetJobByID failed: %v", err)
		}
		if updated.Status != storage.JobStatusQueued {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 'queued', got '%s'", updated.Status)
		}
	})

	t.Run("rerun done job", func(t *testing.T) {
		commit, _ := db.GetOrCreateCommit(repo.ID, "rerun-done", "Author", "Subject", time.Now())
		job, _ := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rerun-done", Agent: "test"})
		// Claim and complete job
		var claimed *storage.ReviewJob
		for {
			claimed, _ = db.ClaimJob("worker-1")
			require.NotNil(t, claimed, "No job to claim")
			if claimed.ID == job.ID {
				break
			}
			db.CompleteJob(claimed.ID, "test", "prompt", "output")
		}
		db.CompleteJob(job.ID, "test", "prompt", "output")

		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/rerun", RerunJobRequest{JobID: job.ID})
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusOK {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		updated, err := db.GetJobByID(job.ID)
		if err != nil {
			require.Condition(t, func() bool {
				return false
			}, "GetJobByID failed: %v", err)
		}
		if updated.Status != storage.JobStatusQueued {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 'queued', got '%s'", updated.Status)
		}
	})

	t.Run("rerun queued job fails", func(t *testing.T) {
		commit, _ := db.GetOrCreateCommit(repo.ID, "rerun-queued", "Author", "Subject", time.Now())
		job, _ := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "rerun-queued", Agent: "test"})

		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/rerun", RerunJobRequest{JobID: job.ID})
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusNotFound {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 404 for queued job, got %d", w.Code)
		}
	})

	t.Run("rerun nonexistent job fails", func(t *testing.T) {
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/rerun", RerunJobRequest{JobID: 99999})
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusNotFound {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 404 for nonexistent job, got %d", w.Code)
		}
	})

	t.Run("rerun with missing job_id fails", func(t *testing.T) {
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/job/rerun", map[string]any{})
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusBadRequest {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 400 for missing job_id, got %d", w.Code)
		}
	})

	t.Run("rerun with invalid method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/job/rerun", nil)
		w := httptest.NewRecorder()

		server.handleRerunJob(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			assert.Condition(t, func() bool {
				return false
			}, "Expected status 405 for GET, got %d", w.Code)
		}
	})
}

// TestHandleAddCommentToJobStates tests that comments can be added to jobs
// in any state: queued, running, done, failed, and canceled.
func TestHandleAddCommentToJobStates(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repo and commit
	repo, err := db.GetOrCreateRepo(filepath.Join(tmpDir, "test-repo"))
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123", "Author", "Test commit", time.Now())
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "GetOrCreateCommit failed: %v", err)
	}

	testCases := []struct {
		name   string
		status storage.JobStatus // empty string means keep as queued
	}{
		{"queued job", ""},
		{"running job", storage.JobStatusRunning},
		{"completed job", storage.JobStatusDone},
		{"failed job", storage.JobStatusFailed},
		{"canceled job", storage.JobStatusCanceled},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a job
			job, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "abc123", Agent: "test-agent"})
			if err != nil {
				require.Condition(t, func() bool {
					return false
				}, "EnqueueJob failed: %v", err)
			}

			// Set job to desired state
			if tc.status != "" {
				setJobStatus(t, db, job.ID, tc.status)
			}

			// Add comment via API
			reqData := AddCommentRequest{
				JobID:     job.ID,
				Commenter: "test-user",
				Comment:   "Test comment for " + tc.name,
			}
			req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/comment", reqData)
			w := httptest.NewRecorder()

			server.handleAddComment(w, req)

			if w.Code != http.StatusCreated {
				assert.Condition(t, func() bool {
					return false
				}, "Expected status 201, got %d: %s", w.Code, w.Body.String())
			}

			// Verify response contains the comment
			var resp storage.Response
			testutil.DecodeJSON(t, w, &resp)
			if resp.Responder != "test-user" {
				assert.Condition(t, func() bool {
					return false
				}, "Expected responder 'test-user', got %q", resp.Responder)
			}
		})
	}
}

// TestHandleAddCommentToNonExistentJob tests that adding a comment to a
// non-existent job returns 404.
func TestHandleAddCommentToNonExistentJob(t *testing.T) {
	server, _, _ := newTestServer(t)

	reqData := AddCommentRequest{
		JobID:     99999,
		Commenter: "test-user",
		Comment:   "This should fail",
	}
	req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/comment", reqData)
	w := httptest.NewRecorder()

	server.handleAddComment(w, req)

	if w.Code != http.StatusNotFound {
		assert.Condition(t, func() bool {
			return false
		}, "Expected status 404, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "job not found") {
		assert.Condition(t, func() bool {
			return false
		}, "Expected 'job not found' error, got: %s", w.Body.String())
	}
}

// TestHandleAddCommentWithoutReview tests that comments can be added to jobs
// that don't have a review yet (job exists but hasn't completed).
func TestHandleAddCommentWithoutReview(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repo, commit, and job (but NO review)
	job := createTestJob(t, db, filepath.Join(tmpDir, "test-repo"), "abc123", "test-agent")

	// Set job to running (no review exists yet)
	setJobStatus(t, db, job.ID, storage.JobStatusRunning)

	// Verify no review exists
	if _, err := db.GetReviewByJobID(job.ID); err == nil {
		require.Condition(t, func() bool {
			return false
		}, "Expected no review to exist for job")
	}

	// Add comment - should succeed even without a review
	reqData := AddCommentRequest{
		JobID:     job.ID,
		Commenter: "test-user",
		Comment:   "Comment on in-progress job without review",
	}
	req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/comment", reqData)
	w := httptest.NewRecorder()

	server.handleAddComment(w, req)

	if w.Code != http.StatusCreated {
		assert.Condition(t, func() bool {
			return false
		}, "Expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify comment was stored
	comments, err := db.GetCommentsForJob(job.ID)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "GetCommentsForJob failed: %v", err)
	}
	if len(comments) != 1 {
		require.Condition(t, func() bool {
			return false
		}, "Expected 1 comment, got %d", len(comments))
	}
	if comments[0].Response != "Comment on in-progress job without review" {
		assert.Condition(t, func() bool {
			return false
		}, "Unexpected comment: %q", comments[0].Response)
	}
}

func TestHandleListCommentsJobIDParsing(t *testing.T) {
	server, _, _ := newTestServer(t)
	testInvalidIDParsing(t, server.handleListComments, "/api/comments?job_id=%s")
}
