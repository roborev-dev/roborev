package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

func TestHandleBatchJobs(t *testing.T) {
	server, db, _ := newTestServer(t)

	repo, err := db.GetOrCreateRepo("/tmp/repo", "")
	if err != nil {
		t.Fatalf("GetOrCreateRepo failed: %v", err)
	}

	// Job 1: with review
	job1, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, GitRef: "abc123", Agent: "test"})
	if err != nil {
		t.Fatal(err)
	}
	setJobStatus(t, db, job1.ID, storage.JobStatusRunning)
	if err := db.CompleteJob(job1.ID, "test", "p1", "o1"); err != nil {
		t.Fatalf("CompleteJob failed for job1: %v", err)
	}

	// Job 2: no review
	job2, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, GitRef: "def456", Agent: "test"})
	if err != nil {
		t.Fatal(err)
	}

	nonExistentJobID := int64(9999)

	t.Run("fetches jobs with and without reviews", func(t *testing.T) {
		reqBody := map[string][]int64{"job_ids": {job1.ID, job2.ID, nonExistentJobID}}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/jobs/batch", reqBody)
		w := httptest.NewRecorder()

		server.handleBatchJobs(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var resp struct {
			Results map[int64]storage.JobWithReview `json:"results"`
		}
		testutil.DecodeJSON(t, w, &resp)

		if len(resp.Results) != 2 {
			t.Fatalf("Expected 2 results, got %d", len(resp.Results))
		}

		// Check job 1 (with review)
		res1, ok := resp.Results[job1.ID]
		if !ok {
			t.Errorf("Result for job %d not found", job1.ID)
		}
		if res1.Review == nil {
			t.Error("Expected review for job 1")
		} else if res1.Review.Output != "o1" {
			t.Errorf("Expected output 'o1', got %q", res1.Review.Output)
		}

		// Check job 2 (no review)
		res2, ok := resp.Results[job2.ID]
		if !ok {
			t.Errorf("Result for job %d not found", job2.ID)
		}
		if res2.Review != nil {
			t.Error("Expected nil review for job 2")
		}
	})

	t.Run("wrong method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs/batch", nil)
		w := httptest.NewRecorder()
		server.handleBatchJobs(w, req)
		testutil.AssertStatusCode(t, w, http.StatusMethodNotAllowed)
	})

	t.Run("empty job_ids fails", func(t *testing.T) {
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/jobs/batch", map[string][]int64{"job_ids": {}})
		w := httptest.NewRecorder()
		server.handleBatchJobs(w, req)
		testutil.AssertStatusCode(t, w, http.StatusBadRequest)
	})

	t.Run("missing job_ids fails", func(t *testing.T) {
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/jobs/batch", map[string]string{})
		w := httptest.NewRecorder()
		server.handleBatchJobs(w, req)
		testutil.AssertStatusCode(t, w, http.StatusBadRequest)
	})

	t.Run("batch size limit enforced", func(t *testing.T) {
		ids := make([]int64, 101)
		for i := range ids {
			ids[i] = int64(i)
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/jobs/batch", map[string][]int64{"job_ids": ids})
		w := httptest.NewRecorder()
		server.handleBatchJobs(w, req)
		testutil.AssertStatusCode(t, w, http.StatusBadRequest)
		if !strings.Contains(w.Body.String(), "too many job IDs") {
			t.Errorf("Expected error message about batch size, got: %s", w.Body.String())
		}
	})
}

func TestHandleRemap(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Set up a git repo so handleRemap can resolve paths
	repoDir := filepath.Join(tmpDir, "remap-repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	// Resolve symlinks to get the canonical path
	// (macOS /var -> /private/var symlink)
	resolvedDir, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		resolvedDir = repoDir
	}

	repo, err := db.GetOrCreateRepo(resolvedDir)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := db.GetOrCreateCommit(repo.ID, "oldsha111", "Test", "old commit", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.EnqueueJob(storage.EnqueueOpts{
		RepoID:   repo.ID,
		CommitID: commit.ID,
		GitRef:   "oldsha111",
		Agent:    "test",
		PatchID:  "patchXYZ",
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("remap updates job", func(t *testing.T) {
		reqData := RemapRequest{
			RepoPath: repoDir,
			Mappings: []RemapMapping{
				{
					OldSHA:    "oldsha111",
					NewSHA:    "newsha222",
					PatchID:   "patchXYZ",
					Author:    "Test",
					Subject:   "new commit",
					Timestamp: time.Now().Format(time.RFC3339),
				},
			},
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/remap", reqData)
		w := httptest.NewRecorder()
		server.handleRemap(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var result struct {
			Remapped int `json:"remapped"`
		}
		testutil.DecodeJSON(t, w, &result)
		if result.Remapped != 1 {
			t.Errorf("expected remapped=1, got %d", result.Remapped)
		}
	})

	t.Run("remap with non-git path returns 400", func(t *testing.T) {
		reqData := RemapRequest{
			RepoPath: "/nonexistent/repo",
			Mappings: []RemapMapping{
				{OldSHA: "a", NewSHA: "b", PatchID: "c", Author: "x", Subject: "y", Timestamp: time.Now().Format(time.RFC3339)},
			},
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/remap", reqData)
		w := httptest.NewRecorder()
		server.handleRemap(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("remap with unregistered repo returns 404", func(t *testing.T) {
		// Create a valid git repo that is NOT registered in the DB
		unregistered := filepath.Join(tmpDir, "unregistered-repo")
		if err := os.MkdirAll(unregistered, 0755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "init")
		cmd.Dir = unregistered
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %s: %v", out, err)
		}

		reqData := RemapRequest{
			RepoPath: unregistered,
			Mappings: []RemapMapping{
				{OldSHA: "a", NewSHA: "b", PatchID: "c", Author: "x", Subject: "y", Timestamp: time.Now().Format(time.RFC3339)},
			},
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/remap", reqData)
		w := httptest.NewRecorder()
		server.handleRemap(w, req)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("remap with invalid timestamp returns 400", func(t *testing.T) {
		reqData := RemapRequest{
			RepoPath: repoDir,
			Mappings: []RemapMapping{
				{
					OldSHA:    "oldsha111",
					NewSHA:    "newsha333",
					PatchID:   "patchXYZ",
					Author:    "Test",
					Subject:   "bad ts",
					Timestamp: "not-a-timestamp",
				},
			},
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/remap", reqData)
		w := httptest.NewRecorder()
		server.handleRemap(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("remap with empty repo_path returns 400", func(t *testing.T) {
		reqData := RemapRequest{
			RepoPath: "",
			Mappings: []RemapMapping{
				{
					OldSHA:    "a",
					NewSHA:    "b",
					PatchID:   "c",
					Author:    "x",
					Subject:   "y",
					Timestamp: time.Now().Format(time.RFC3339),
				},
			},
		}
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/remap", reqData,
		)
		w := httptest.NewRecorder()
		server.handleRemap(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s",
				w.Code, w.Body.String())
		}
	})

	t.Run("remap rejects too many mappings", func(t *testing.T) {
		mappings := make([]RemapMapping, 1001)
		for i := range mappings {
			mappings[i] = RemapMapping{
				OldSHA: "a", NewSHA: "b", PatchID: "c",
				Author: "x", Subject: "y",
				Timestamp: time.Now().Format(time.RFC3339),
			}
		}
		reqData := RemapRequest{
			RepoPath: repoDir,
			Mappings: mappings,
		}
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/remap", reqData,
		)
		w := httptest.NewRecorder()
		server.handleRemap(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s",
				w.Code, w.Body.String())
		}
	})

	t.Run("remap rejects oversized body", func(t *testing.T) {
		// Build a payload larger than 1MB.
		// JSON-encode repoDir so Windows backslashes are escaped.
		escapedDir, _ := json.Marshal(repoDir)
		body := []byte(`{"repo_path":` + string(escapedDir) + `,"mappings":[`)
		entry := []byte(`{"old_sha":"a","new_sha":"b","patch_id":"c","author":"x","subject":"y","timestamp":"2026-01-01T00:00:00Z"},`)
		for len(body) < 1<<20+1 {
			body = append(body, entry...)
		}
		body = append(body[:len(body)-1], []byte(`]}`)...)

		req := httptest.NewRequest(
			http.MethodPost, "/api/remap",
			bytes.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.handleRemap(w, req)

		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413, got %d: %s",
				w.Code, w.Body.String())
		}
	})
}

func TestHandleGetReviewJobIDParsing(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	repoPath := filepath.Join(tmpDir, "repo-review-parse")
	repo, err := db.GetOrCreateRepo(repoPath)
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	commit, err := db.GetOrCreateCommit(
		repo.ID, "abc123", "msg", "Author", time.Now(),
	)
	if err != nil {
		t.Fatalf("GetOrCreateCommit: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{
		RepoID:   repo.ID,
		CommitID: commit.ID,
		GitRef:   "abc123",
		Branch:   "main",
		Agent:    "test",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	claimed, err := db.ClaimJob("test-worker")
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if claimed == nil || claimed.ID != job.ID {
		t.Fatalf("ClaimJob: expected job %d", job.ID)
	}
	if err := db.CompleteJob(
		job.ID, "test", "prompt", "review output",
	); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}

	tests := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{
			"valid job_id",
			fmt.Sprintf("job_id=%d", job.ID),
			http.StatusOK,
		},
		{
			"missing params",
			"",
			http.StatusBadRequest,
		},
		{
			"non-numeric job_id",
			"job_id=abc",
			http.StatusBadRequest,
		},
		{
			"partial numeric job_id",
			"job_id=10abc",
			http.StatusBadRequest,
		},
		{
			"not found job_id",
			"job_id=999999",
			http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/api/review"
			if tt.query != "" {
				url += "?" + tt.query
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			w := httptest.NewRecorder()

			server.handleGetReview(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf(
					"expected status %d, got %d: %s",
					tt.wantStatus, w.Code, w.Body.String(),
				)
			}
		})
	}
}

// fixJobRequest mirrors the anonymous request struct in handleFixJob.
type fixJobRequest struct {
	ParentJobID int64  `json:"parent_job_id"`
	Prompt      string `json:"prompt,omitempty"`
	GitRef      string `json:"git_ref,omitempty"`
	StaleJobID  int64  `json:"stale_job_id,omitempty"`
}

func TestHandleFixJobStaleValidation(t *testing.T) {
	server, db, tmpDir := newTestServer(t)
	server.configWatcher.Config().FixAgent = "test"

	repoDir := filepath.Join(tmpDir, "repo-fix-val")
	testutil.InitTestGitRepo(t, repoDir)
	repo, _ := db.GetOrCreateRepo(repoDir)
	commit, _ := db.GetOrCreateCommit(
		repo.ID, "fix-val-abc", "Author", "Subject", time.Now(),
	)

	// Create a review job and complete it with output
	reviewJob, _ := db.EnqueueJob(storage.EnqueueOpts{
		RepoID:   repo.ID,
		CommitID: commit.ID,
		GitRef:   "fix-val-abc",
		Agent:    "test",
	})
	db.ClaimJob("w1")
	db.CompleteJob(reviewJob.ID, "test", "prompt", "FAIL: issues found")

	t.Run("fix job inherits parent commit metadata", func(t *testing.T) {
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix",
			fixJobRequest{ParentJobID: reviewJob.ID},
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf(
				"Expected 201 for fix enqueue, got %d: %s",
				w.Code, w.Body.String(),
			)
		}

		var fixJob storage.ReviewJob
		testutil.DecodeJSON(t, w, &fixJob)
		if fixJob.CommitID == nil || *fixJob.CommitID != commit.ID {
			t.Fatalf(
				"Expected fix job commit_id=%d, got %v",
				commit.ID, fixJob.CommitID,
			)
		}
		if fixJob.CommitSubject != commit.Subject {
			t.Fatalf(
				"Expected fix job commit_subject %q, got %q",
				commit.Subject, fixJob.CommitSubject,
			)
		}

		stored, err := db.GetJobByID(fixJob.ID)
		if err != nil {
			t.Fatalf("GetJobByID(%d): %v", fixJob.ID, err)
		}
		if stored.CommitID == nil || *stored.CommitID != commit.ID {
			t.Fatalf(
				"Expected stored fix job commit_id=%d, got %v",
				commit.ID, stored.CommitID,
			)
		}
		if stored.CommitSubject != commit.Subject {
			t.Fatalf(
				"Expected stored fix job commit_subject %q, got %q",
				commit.Subject, stored.CommitSubject,
			)
		}
	})

	t.Run("custom prompt includes review context", func(t *testing.T) {
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix",
			fixJobRequest{
				ParentJobID: reviewJob.ID,
				Prompt:      "Ignore the security concern, it's only a testing binary.",
			},
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf(
				"Expected 201 for fix enqueue with custom prompt, got %d: %s",
				w.Code, w.Body.String(),
			)
		}

		var fixJob storage.ReviewJob
		testutil.DecodeJSON(t, w, &fixJob)

		stored, err := db.GetJobByID(fixJob.ID)
		if err != nil {
			t.Fatalf("GetJobByID(%d): %v", fixJob.ID, err)
		}

		// The stored prompt must contain both the review output AND the custom instructions
		if !strings.Contains(stored.Prompt, "FAIL: issues found") {
			t.Fatalf(
				"Expected fix prompt to contain review output, got:\n%s",
				stored.Prompt,
			)
		}
		if !strings.Contains(stored.Prompt, "Ignore the security concern") {
			t.Fatalf(
				"Expected fix prompt to contain custom instructions, got:\n%s",
				stored.Prompt,
			)
		}
	})

	t.Run("fix job as parent is rejected", func(t *testing.T) {
		// Create a fix job and try to use it as a parent
		fixJob, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:      repo.ID,
			CommitID:    commit.ID,
			GitRef:      "fix-val-abc",
			Agent:       "test",
			JobType:     storage.JobTypeFix,
			ParentJobID: reviewJob.ID,
		})
		db.ClaimJob("w-fix-parent")
		db.CompleteJob(fixJob.ID, "test", "prompt", "done")

		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix",
			fixJobRequest{ParentJobID: fixJob.ID},
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf(
				"Expected 400 for fix-job parent, got %d: %s",
				w.Code, w.Body.String(),
			)
		}
	})

	t.Run("stale job that is not a fix job is rejected", func(t *testing.T) {
		// reviewJob is a review, not a fix job
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix",
			fixJobRequest{ParentJobID: reviewJob.ID, StaleJobID: reviewJob.ID},
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 for non-fix stale job, got %d: %s",
				w.Code, w.Body.String())
		}
	})

	t.Run("stale job with wrong parent is rejected", func(t *testing.T) {
		// Create a second review + fix in the SAME repo, linked to the other review
		review2, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:   repo.ID,
			CommitID: commit.ID,
			GitRef:   "fix-val-def",
			Agent:    "test",
		})
		db.ClaimJob("w3")
		db.CompleteJob(review2.ID, "test", "prompt", "FAIL: other issues")

		wrongParentFix, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:      repo.ID,
			CommitID:    commit.ID,
			GitRef:      "fix-val-def",
			Agent:       "test",
			JobType:     storage.JobTypeFix,
			ParentJobID: review2.ID,
		})
		// Complete it so it has terminal status + patch
		db.ClaimJob("w4")
		db.CompleteJob(wrongParentFix.ID, "test", "prompt", "done")
		db.SaveJobPatch(wrongParentFix.ID, "--- a/f\n+++ b/f\n")

		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix",
			fixJobRequest{ParentJobID: reviewJob.ID, StaleJobID: wrongParentFix.ID},
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 for wrong-parent stale job, got %d: %s",
				w.Code, w.Body.String())
		}
	})

	t.Run("stale job without patch is rejected", func(t *testing.T) {
		// Create a fix job linked to reviewJob but with no patch
		noPatchFix, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:      repo.ID,
			CommitID:    commit.ID,
			GitRef:      "fix-val-abc",
			Agent:       "test",
			JobType:     storage.JobTypeFix,
			ParentJobID: reviewJob.ID,
		})
		// Complete it (terminal status) but don't set a patch
		db.ClaimJob("w5")
		db.CompleteJob(noPatchFix.ID, "test", "prompt", "done but no diff")

		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix",
			fixJobRequest{ParentJobID: reviewJob.ID, StaleJobID: noPatchFix.ID},
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 for patchless stale job, got %d: %s",
				w.Code, w.Body.String())
		}
	})

	t.Run("non-terminal stale job statuses are rejected", func(t *testing.T) {
		for _, status := range []storage.JobStatus{
			storage.JobStatusQueued,
			storage.JobStatusRunning,
			storage.JobStatusFailed,
			storage.JobStatusCanceled,
		} {
			t.Run(string(status), func(t *testing.T) {
				fixJob, err := db.EnqueueJob(storage.EnqueueOpts{
					RepoID:      repo.ID,
					CommitID:    commit.ID,
					GitRef:      "fix-val-abc",
					Agent:       "test",
					JobType:     storage.JobTypeFix,
					ParentJobID: reviewJob.ID,
				})
				if err != nil {
					t.Fatalf("EnqueueJob: %v", err)
				}

				// Set status directly to avoid ClaimJob picking
				// a different queued job from an earlier iteration.
				if status != storage.JobStatusQueued {
					_, err = db.Exec(
						`UPDATE review_jobs SET status = ? WHERE id = ?`,
						string(status), fixJob.ID,
					)
					if err != nil {
						t.Fatalf("Set status to %s: %v", status, err)
					}
				}

				// Verify the job is in the expected state
				got, err := db.GetJobByID(fixJob.ID)
				if err != nil {
					t.Fatalf("GetJobByID: %v", err)
				}
				if got.Status != status {
					t.Fatalf(
						"Expected job %d status %s, got %s",
						fixJob.ID, status, got.Status,
					)
				}

				req := testutil.MakeJSONRequest(
					t, http.MethodPost, "/api/job/fix",
					fixJobRequest{ParentJobID: reviewJob.ID, StaleJobID: fixJob.ID},
				)
				w := httptest.NewRecorder()
				server.handleFixJob(w, req)

				if w.Code != http.StatusBadRequest {
					t.Errorf(
						"Expected 400 for %s stale job, got %d: %s",
						status, w.Code, w.Body.String(),
					)
				}
			})
		}
	})

	t.Run("compact parent with empty git_ref uses branch as fallback", func(t *testing.T) {
		// Compact jobs are stored with an empty git_ref (the label is stored separately).
		// Fixing a compact job must not pass "" to worktree.Create — it should fall back
		// to the branch, then "HEAD".
		compactJob, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:  repo.ID,
			GitRef:  "", // compact jobs have no real git ref
			Branch:  "main",
			Agent:   "test",
			JobType: storage.JobTypeCompact,
			Prompt:  "consolidated findings",
			Label:   "compact-all-20240101-120000",
		})
		// Force to running so CompleteJob can transition it to done.
		setJobStatus(t, db, compactJob.ID, storage.JobStatusRunning)
		db.CompleteJob(compactJob.ID, "test", "consolidated findings", "FAIL: issues found")

		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix",
			fixJobRequest{ParentJobID: compactJob.ID},
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201 for compact parent fix, got %d: %s", w.Code, w.Body.String())
		}

		var fixJob storage.ReviewJob
		testutil.DecodeJSON(t, w, &fixJob)

		if fixJob.GitRef == "" {
			t.Errorf("Fix job git_ref must not be empty for compact parent")
		}
		if fixJob.GitRef != "main" {
			t.Errorf("Expected fix job git_ref 'main' (branch fallback), got %q", fixJob.GitRef)
		}
	})

	t.Run("range parent uses branch instead of range ref for fix worktree", func(t *testing.T) {
		// Range git refs like "sha1..sha2" are not valid for git worktree add.
		// Fixing a range review should use the branch instead.
		rangeJob, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID: repo.ID,
			GitRef: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa..bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Branch: "feature/foo",
			Agent:  "test",
		})
		setJobStatus(t, db, rangeJob.ID, storage.JobStatusRunning)
		db.CompleteJob(rangeJob.ID, "test", "prompt", "FAIL: issues found")

		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix",
			fixJobRequest{ParentJobID: rangeJob.ID},
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201 for range parent fix, got %d: %s", w.Code, w.Body.String())
		}

		var fixJob storage.ReviewJob
		testutil.DecodeJSON(t, w, &fixJob)

		if strings.Contains(fixJob.GitRef, "..") {
			t.Errorf("Fix job git_ref must not be a range, got %q", fixJob.GitRef)
		}
		if fixJob.GitRef != "feature/foo" {
			t.Errorf("Expected fix job git_ref 'feature/foo' (branch fallback), got %q", fixJob.GitRef)
		}
	})

	t.Run("rejects git_ref starting with dash", func(t *testing.T) {
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix",
			fixJobRequest{ParentJobID: reviewJob.ID, GitRef: "--option-injection"},
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("Expected 400 for dash-prefixed git_ref, got %d", w.Code)
		}
	})

	t.Run("rejects git_ref with control chars", func(t *testing.T) {
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix",
			fixJobRequest{ParentJobID: reviewJob.ID, GitRef: "main\x00injected"},
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("Expected 400 for control-char git_ref, got %d", w.Code)
		}
	})

	t.Run("rejects whitespace-padded dash ref", func(t *testing.T) {
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix",
			fixJobRequest{ParentJobID: reviewJob.ID, GitRef: " --option-injection"},
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf(
				"Expected 400 for space+dash git_ref, got %d",
				w.Code,
			)
		}
	})

	t.Run("treats whitespace-only git_ref as empty", func(t *testing.T) {
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix",
			fixJobRequest{ParentJobID: reviewJob.ID, GitRef: "   "},
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		// Whitespace-only trims to empty, so it's treated as
		// no user-provided ref — the server falls through to
		// the parent ref/branch/HEAD resolution chain.
		if w.Code != http.StatusCreated {
			t.Fatalf(
				"Expected 201 for whitespace-only git_ref (treated as empty), got %d: %s",
				w.Code, w.Body.String(),
			)
		}
	})

	t.Run("accepts valid git_ref from request", func(t *testing.T) {
		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix",
			fixJobRequest{ParentJobID: reviewJob.ID, GitRef: "feature/my-branch"},
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201 for valid git_ref, got %d: %s", w.Code, w.Body.String())
		}

		var fixJob storage.ReviewJob
		testutil.DecodeJSON(t, w, &fixJob)
		if fixJob.GitRef != "feature/my-branch" {
			t.Errorf("Expected git_ref 'feature/my-branch', got %q", fixJob.GitRef)
		}
	})

	t.Run("stale job from different repo is rejected", func(t *testing.T) {
		// Create a fix job in a different repo
		repo2Dir := filepath.Join(tmpDir, "repo-fix-val-2")
		testutil.InitTestGitRepo(t, repo2Dir)
		repo2, _ := db.GetOrCreateRepo(repo2Dir)
		commit2, _ := db.GetOrCreateCommit(
			repo2.ID, "other-sha", "Author", "Subject", time.Now(),
		)
		otherReview, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:   repo2.ID,
			CommitID: commit2.ID,
			GitRef:   "other-sha",
			Agent:    "test",
		})
		db.ClaimJob("w2")
		db.CompleteJob(otherReview.ID, "test", "prompt", "FAIL")

		otherFix, _ := db.EnqueueJob(storage.EnqueueOpts{
			RepoID:      repo2.ID,
			CommitID:    commit2.ID,
			GitRef:      "other-sha",
			Agent:       "test",
			JobType:     storage.JobTypeFix,
			ParentJobID: otherReview.ID,
		})

		req := testutil.MakeJSONRequest(
			t, http.MethodPost, "/api/job/fix",
			fixJobRequest{ParentJobID: reviewJob.ID, StaleJobID: otherFix.ID},
		)
		w := httptest.NewRecorder()
		server.handleFixJob(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 for cross-repo stale job, got %d: %s",
				w.Code, w.Body.String())
		}
	})
}
