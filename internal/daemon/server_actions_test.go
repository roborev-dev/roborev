package daemon

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

func TestHandleStatus(t *testing.T) {
	server, _, _ := newTestServer(t)

	t.Run("returns status with version", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		w := httptest.NewRecorder()

		server.handleStatus(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var status storage.DaemonStatus
		testutil.DecodeJSON(t, w, &status)

		// Version should be set (non-empty)
		if status.Version == "" {
			t.Error("Expected Version to be set in status response")
		}
	})

	t.Run("wrong method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/status", nil)
		w := httptest.NewRecorder()

		server.handleStatus(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected status 405 for POST, got %d", w.Code)
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
			t.Errorf("Expected MaxWorkers %d from pool, got %d", expectedWorkers, status.MaxWorkers)
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
			t.Errorf("Expected ConfigReloadedAt to be empty initially, got %q", status.ConfigReloadedAt)
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
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var ping PingInfo
		testutil.DecodeJSON(t, w, &ping)
		if ping.Service != daemonServiceName {
			t.Fatalf("Expected service %q, got %q", daemonServiceName, ping.Service)
		}
		if ping.Version == "" {
			t.Fatal("Expected ping version to be set")
		}
		if ping.PID == 0 {
			t.Fatal("Expected ping PID to be set")
		}
	})

	t.Run("wrong method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/ping", nil)
		w := httptest.NewRecorder()

		server.handlePing(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("Expected status 405, got %d", w.Code)
		}
	})
}

func assertHandlerStatus(t *testing.T, handler http.HandlerFunc, req *http.Request, wantCode int) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != wantCode {
		t.Fatalf("Expected status %d, got %d: %s", wantCode, w.Code, w.Body.String())
	}
	return w
}

func createJobWithStatus(t *testing.T, db *storage.DB, repoID int64, ref string, status storage.JobStatus) *storage.ReviewJob {
	t.Helper()
	commit, err := db.GetOrCreateCommit(repoID, ref, "Author", "Subject", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repoID, CommitID: commit.ID, GitRef: ref, Agent: "test"})
	if err != nil {
		t.Fatalf("EnqueueJob failed: %v", err)
	}

	switch status {
	case storage.JobStatusRunning:
		var skipped []int64
		var claimed *storage.ReviewJob
		workerIdx := 1
		for {
			var err error
			workerID := fmt.Sprintf("worker-%d", workerIdx)
			workerIdx++
			claimed, err = db.ClaimJob(workerID)
			if err != nil {
				t.Fatalf("ClaimJob failed: %v", err)
			}
			if claimed == nil {
				t.Fatalf("ClaimJob found no jobs but wanted %v", job.ID)
			}
			if claimed.ID == job.ID {
				break
			}
			skipped = append(skipped, claimed.ID)
		}
		for _, id := range skipped {
			if err := db.CancelJob(id); err != nil {
				t.Fatalf("CancelJob before ReenqueueJob failed: %v", err)
			}
			if err := db.ReenqueueJob(id); err != nil {
				t.Fatalf("ReenqueueJob failed: %v", err)
			}
		}
	case storage.JobStatusFailed:
		var skipped []int64
		var claimed *storage.ReviewJob
		workerIdx := 1
		for {
			var err error
			workerID := fmt.Sprintf("worker-%d", workerIdx)
			workerIdx++
			claimed, err = db.ClaimJob(workerID)
			if err != nil {
				t.Fatalf("ClaimJob failed: %v", err)
			}
			if claimed == nil {
				t.Fatalf("ClaimJob found no jobs but wanted %v", job.ID)
			}
			if claimed.ID == job.ID {
				break
			}
			skipped = append(skipped, claimed.ID)
		}
		for _, id := range skipped {
			if err := db.CancelJob(id); err != nil {
				t.Fatalf("CancelJob before ReenqueueJob failed: %v", err)
			}
			if err := db.ReenqueueJob(id); err != nil {
				t.Fatalf("ReenqueueJob failed: %v", err)
			}
		}
		db.FailJob(job.ID, "", "some error")
	case storage.JobStatusCanceled:
		db.CancelJob(job.ID)
	case storage.JobStatusDone:
		for {
			claimed, err := db.ClaimJob("worker-1")
			if err != nil || claimed == nil {
				t.Fatalf("ClaimJob failed: %v", err)
			}
			if claimed.ID == job.ID {
				break
			}
			db.CompleteJob(claimed.ID, "test", "prompt", "output")
		}
		db.CompleteJob(job.ID, "test", "prompt", "output")
	}

	return job
}

func TestHandleCancelJob(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		setupJob   func(t *testing.T, db *storage.DB, repoID int64) *storage.ReviewJob
		payloadFn  func(jobID int64) any
		wantStatus int
	}{
		{
			"cancel queued job",
			http.MethodPost,
			func(t *testing.T, db *storage.DB, repoID int64) *storage.ReviewJob {
				return createJobWithStatus(t, db, repoID, "cancelqueued", storage.JobStatusQueued)
			},
			func(id int64) any { return CancelJobRequest{JobID: id} },
			http.StatusOK,
		},
		{
			"cancel already canceled job fails",
			http.MethodPost,
			func(t *testing.T, db *storage.DB, repoID int64) *storage.ReviewJob {
				return createJobWithStatus(t, db, repoID, "cancelcanceled", storage.JobStatusCanceled)
			},
			func(id int64) any { return CancelJobRequest{JobID: id} },
			http.StatusNotFound,
		},
		{
			"cancel nonexistent job fails",
			http.MethodPost,
			nil,
			func(id int64) any { return CancelJobRequest{JobID: 99999} },
			http.StatusNotFound,
		},
		{
			"cancel with missing job_id fails",
			http.MethodPost,
			nil,
			func(id int64) any { return map[string]any{} },
			http.StatusBadRequest,
		},
		{
			"cancel with wrong method fails",
			http.MethodGet,
			nil,
			func(id int64) any { return nil },
			http.StatusMethodNotAllowed,
		},
		{
			"cancel running job",
			http.MethodPost,
			func(t *testing.T, db *storage.DB, repoID int64) *storage.ReviewJob {
				return createJobWithStatus(t, db, repoID, "cancelrunning", storage.JobStatusRunning)
			},
			func(id int64) any { return CancelJobRequest{JobID: id} },
			http.StatusOK,
		},
		{
			"cancel running job with older queued job present",
			http.MethodPost,
			func(t *testing.T, db *storage.DB, repoID int64) *storage.ReviewJob {
				createJobWithStatus(t, db, repoID, "olderqueued", storage.JobStatusQueued)
				return createJobWithStatus(t, db, repoID, "cancelrunning-multi", storage.JobStatusRunning)
			},
			func(id int64) any { return CancelJobRequest{JobID: id} },
			http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, db, tmpDir := newTestServer(t)

			repo, err := db.GetOrCreateRepo(tmpDir)
			if err != nil {
				t.Fatalf("GetOrCreateRepo failed: %v", err)
			}

			var checkJob *storage.ReviewJob
			if tt.setupJob != nil {
				checkJob = tt.setupJob(t, db, repo.ID)
			}

			var payload any
			if checkJob != nil {
				payload = tt.payloadFn(checkJob.ID)
			} else {
				payload = tt.payloadFn(0)
			}

			var req *http.Request
			if payload != nil {
				req = testutil.MakeJSONRequest(t, tt.method, "/api/job/cancel", payload)
			} else {
				req = httptest.NewRequest(tt.method, "/api/job/cancel", nil)
			}
			assertHandlerStatus(t, server.handleCancelJob, req, tt.wantStatus)

			if checkJob != nil && tt.wantStatus == http.StatusOK {
				updated, err := db.GetJobByID(checkJob.ID)
				if err != nil {
					t.Fatalf("GetJobByID failed: %v", err)
				}
				if updated.Status != storage.JobStatusCanceled {
					t.Errorf("Expected status 'canceled', got '%s'", updated.Status)
				}
			}
		})
	}
}

func TestHandleRerunJob(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		setupJob   func(t *testing.T, db *storage.DB, repoID int64) *storage.ReviewJob
		payloadFn  func(jobID int64) any
		wantStatus int
	}{
		{
			"rerun failed job",
			http.MethodPost,
			func(t *testing.T, db *storage.DB, repoID int64) *storage.ReviewJob {
				return createJobWithStatus(t, db, repoID, "rerun-failed", storage.JobStatusFailed)
			},
			func(id int64) any { return RerunJobRequest{JobID: id} },
			http.StatusOK,
		},
		{
			"rerun failed job with older queued job present",
			http.MethodPost,
			func(t *testing.T, db *storage.DB, repoID int64) *storage.ReviewJob {
				createJobWithStatus(t, db, repoID, "olderqueued", storage.JobStatusQueued)
				return createJobWithStatus(t, db, repoID, "rerun-failed-multi", storage.JobStatusFailed)
			},
			func(id int64) any { return RerunJobRequest{JobID: id} },
			http.StatusOK,
		},
		{
			"rerun canceled job",
			http.MethodPost,
			func(t *testing.T, db *storage.DB, repoID int64) *storage.ReviewJob {
				return createJobWithStatus(t, db, repoID, "rerun-canceled", storage.JobStatusCanceled)
			},
			func(id int64) any { return RerunJobRequest{JobID: id} },
			http.StatusOK,
		},
		{
			"rerun done job",
			http.MethodPost,
			func(t *testing.T, db *storage.DB, repoID int64) *storage.ReviewJob {
				return createJobWithStatus(t, db, repoID, "rerun-done", storage.JobStatusDone)
			},
			func(id int64) any { return RerunJobRequest{JobID: id} },
			http.StatusOK,
		},
		{
			"rerun queued job fails",
			http.MethodPost,
			func(t *testing.T, db *storage.DB, repoID int64) *storage.ReviewJob {
				return createJobWithStatus(t, db, repoID, "rerun-queued", storage.JobStatusQueued)
			},
			func(id int64) any { return RerunJobRequest{JobID: id} },
			http.StatusNotFound,
		},
		{
			"rerun nonexistent job fails",
			http.MethodPost,
			nil,
			func(id int64) any { return RerunJobRequest{JobID: 99999} },
			http.StatusNotFound,
		},
		{
			"rerun with missing job_id fails",
			http.MethodPost,
			nil,
			func(id int64) any { return map[string]any{} },
			http.StatusBadRequest,
		},
		{
			"rerun with invalid method fails",
			http.MethodGet,
			nil,
			func(id int64) any { return nil },
			http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, db, tmpDir := newTestServer(t)

			repo, err := db.GetOrCreateRepo(tmpDir)
			if err != nil {
				t.Fatalf("GetOrCreateRepo failed: %v", err)
			}

			var checkJob *storage.ReviewJob
			if tt.setupJob != nil {
				checkJob = tt.setupJob(t, db, repo.ID)
			}

			var payload any
			if checkJob != nil {
				payload = tt.payloadFn(checkJob.ID)
			} else {
				payload = tt.payloadFn(0)
			}

			var req *http.Request
			if payload != nil {
				req = testutil.MakeJSONRequest(t, tt.method, "/api/job/rerun", payload)
			} else {
				req = httptest.NewRequest(tt.method, "/api/job/rerun", nil)
			}
			assertHandlerStatus(t, server.handleRerunJob, req, tt.wantStatus)

			if checkJob != nil && tt.wantStatus == http.StatusOK {
				updated, err := db.GetJobByID(checkJob.ID)
				if err != nil {
					t.Fatalf("GetJobByID failed: %v", err)
				}
				if updated.Status != storage.JobStatusQueued {
					t.Errorf("Expected status 'queued', got '%s'", updated.Status)
				}
			}
		})
	}
}

// TestHandleAddCommentToJobStates tests that comments can be added to jobs
// in any state: queued, running, done, failed, and canceled.
func TestHandleAddCommentToJobStates(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repo and commit
	repo, err := db.GetOrCreateRepo(filepath.Join(tmpDir, "test-repo"))
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "abc123", "Author", "Test commit", time.Now())
	if err != nil {
		t.Fatalf("GetOrCreateCommit failed: %v", err)
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
				t.Fatalf("EnqueueJob failed: %v", err)
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
				t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
			}

			// Verify response contains the comment
			var resp storage.Response
			testutil.DecodeJSON(t, w, &resp)
			if resp.Responder != "test-user" {
				t.Errorf("Expected responder 'test-user', got %q", resp.Responder)
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
		t.Errorf("Expected status 404, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "job not found") {
		t.Errorf("Expected 'job not found' error, got: %s", w.Body.String())
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
		t.Fatal("Expected no review to exist for job")
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
		t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify comment was stored
	comments, err := db.GetCommentsForJob(job.ID)
	if err != nil {
		t.Fatalf("GetCommentsForJob failed: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("Expected 1 comment, got %d", len(comments))
	}
	if comments[0].Response != "Comment on in-progress job without review" {
		t.Errorf("Unexpected comment: %q", comments[0].Response)
	}
}

func TestHandleListCommentsJobIDParsing(t *testing.T) {
	server, _, _ := newTestServer(t)
	testInvalidIDParsing(t, server.handleListComments, "/api/comments?job_id=%s")
}
