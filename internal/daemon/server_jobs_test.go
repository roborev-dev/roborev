package daemon

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	gitpkg "github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

func TestHandleListJobsWithFilter(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repos and jobs
	repo1, _ := seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo1"), 3, "repo1")
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo2"), 2, "repo2")

	t.Run("no filter returns all jobs", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 5 {
			t.Errorf("Expected 5 jobs, got %d", len(response.Jobs))
		}
	})

	t.Run("repo filter returns only matching jobs", func(t *testing.T) {
		// Filter by root_path (not name) since repos with same name could exist at different paths
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?repo="+url.QueryEscape(repo1.RootPath), nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 3 {
			t.Errorf("Expected 3 jobs for repo1, got %d", len(response.Jobs))
		}

		// Verify all jobs are from repo1
		for _, job := range response.Jobs {
			if job.RepoName != "repo1" {
				t.Errorf("Expected RepoName 'repo1', got '%s'", job.RepoName)
			}
		}
	})

	t.Run("limit parameter works", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?limit=2", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 2 {
			t.Errorf("Expected 2 jobs with limit=2, got %d", len(response.Jobs))
		}
	})

	t.Run("limit=0 returns all jobs", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?limit=0", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 5 {
			t.Errorf("Expected 5 jobs with limit=0 (no limit), got %d", len(response.Jobs))
		}
	})

	t.Run("repo filter with limit", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?repo="+url.QueryEscape(repo1.RootPath)+"&limit=2", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 2 {
			t.Errorf("Expected 2 jobs with repo filter and limit=2, got %d", len(response.Jobs))
		}

		// Verify all jobs are from repo1
		for _, job := range response.Jobs {
			if job.RepoName != "repo1" {
				t.Errorf("Expected RepoName 'repo1', got '%s'", job.RepoName)
			}
		}
	})

	t.Run("negative limit treated as unlimited", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?limit=-1", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		// Negative clamped to 0 (unlimited), should return all 5 jobs
		if len(response.Jobs) != 5 {
			t.Errorf("Expected 5 jobs with limit=-1 (clamped to unlimited), got %d", len(response.Jobs))
		}
	})

	t.Run("very large limit capped to max", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?limit=999999", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		// Large limit capped to 10000, but we only have 5 jobs
		if len(response.Jobs) != 5 {
			t.Errorf("Expected 5 jobs (all available), got %d", len(response.Jobs))
		}
	})

	t.Run("invalid limit uses default", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?limit=abc", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		// Invalid limit uses default (50), we have 5 jobs
		if len(response.Jobs) != 5 {
			t.Errorf("Expected 5 jobs with invalid limit (uses default), got %d", len(response.Jobs))
		}
	})
}

func TestListJobsPagination(t *testing.T) {
	server, db, _ := newTestServer(t)

	// Create test repo and 10 jobs
	seedRepoWithJobs(t, db, "/test/repo", 10, "")

	t.Run("has_more true when more jobs exist", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/jobs?limit=5", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", w.Code)
		}

		var result struct {
			Jobs    []storage.ReviewJob `json:"jobs"`
			HasMore bool                `json:"has_more"`
		}
		testutil.DecodeJSON(t, w, &result)

		if len(result.Jobs) != 5 {
			t.Errorf("Expected 5 jobs, got %d", len(result.Jobs))
		}
		if !result.HasMore {
			t.Error("Expected has_more=true when more jobs exist")
		}
	})

	t.Run("has_more false when no more jobs", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/jobs?limit=50", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", w.Code)
		}

		var result struct {
			Jobs    []storage.ReviewJob `json:"jobs"`
			HasMore bool                `json:"has_more"`
		}
		testutil.DecodeJSON(t, w, &result)

		if len(result.Jobs) != 10 {
			t.Errorf("Expected 10 jobs, got %d", len(result.Jobs))
		}
		if result.HasMore {
			t.Error("Expected has_more=false when all jobs returned")
		}
	})

	t.Run("offset skips jobs", func(t *testing.T) {
		// First page
		req1 := httptest.NewRequest("GET", "/api/jobs?limit=3&offset=0", nil)
		w1 := httptest.NewRecorder()
		server.handleListJobs(w1, req1)

		var result1 struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w1, &result1)

		// Second page
		req2 := httptest.NewRequest("GET", "/api/jobs?limit=3&offset=3", nil)
		w2 := httptest.NewRecorder()
		server.handleListJobs(w2, req2)

		var result2 struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w2, &result2)

		// Ensure no overlap
		for _, j1 := range result1.Jobs {
			for _, j2 := range result2.Jobs {
				if j1.ID == j2.ID {
					t.Errorf("Job %d appears in both pages", j1.ID)
				}
			}
		}
	})

	t.Run("offset ignored when limit=0", func(t *testing.T) {
		// limit=0 means unlimited, offset should be ignored
		req := httptest.NewRequest("GET", "/api/jobs?limit=0&offset=5", nil)
		w := httptest.NewRecorder()

		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", w.Code)
		}

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &result)

		// Should return all 10 jobs since offset is ignored with limit=0
		if len(result.Jobs) != 10 {
			t.Errorf("Expected 10 jobs (offset ignored with limit=0), got %d", len(result.Jobs))
		}
	})
}

func TestListJobsWithGitRefFilter(t *testing.T) {
	server, db, _ := newTestServer(t)

	// Create repo and jobs with different git refs
	repo, _ := db.GetOrCreateRepo("/tmp/test-repo")
	refs := []string{"abc123", "def456", "abc123..def456"}
	for _, ref := range refs {
		commit, _ := db.GetOrCreateCommit(repo.ID, ref, "A", "S", time.Now())
		db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: ref, Agent: "codex"})
	}

	t.Run("git_ref filter returns matching job", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?git_ref=abc123", nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", w.Code)
		}

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &result)

		if len(result.Jobs) != 1 {
			t.Errorf("Expected 1 job, got %d", len(result.Jobs))
		}
		if len(result.Jobs) > 0 && result.Jobs[0].GitRef != "abc123" {
			t.Errorf("Expected GitRef 'abc123', got '%s'", result.Jobs[0].GitRef)
		}
	})

	t.Run("git_ref filter with no match returns empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?git_ref=nonexistent", nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &result)

		if len(result.Jobs) != 0 {
			t.Errorf("Expected 0 jobs, got %d", len(result.Jobs))
		}
	})

	t.Run("git_ref filter with range ref", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?git_ref="+url.QueryEscape("abc123..def456"), nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &result)

		if len(result.Jobs) != 1 {
			t.Errorf("Expected 1 job with range ref, got %d", len(result.Jobs))
		}
	})
}

func TestHandleListJobsAddressedFilter(t *testing.T) {
	db := testutil.OpenTestDB(t)
	cfg := config.DefaultConfig()
	server := NewServer(db, cfg, "")

	repo, _ := db.GetOrCreateRepo("/tmp/repo-addr-filter")
	commit, _ := db.GetOrCreateCommit(repo.ID, "aaa", "A", "S", time.Now())
	job1, _ := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit.ID, GitRef: "aaa", Branch: "main", Agent: "codex"})
	db.ClaimJob("w")
	db.CompleteJob(job1.ID, "codex", "", "output1")

	commit2, _ := db.GetOrCreateCommit(repo.ID, "bbb", "A", "S2", time.Now())
	job2, _ := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, CommitID: commit2.ID, GitRef: "bbb", Branch: "main", Agent: "codex"})
	db.ClaimJob("w")
	db.CompleteJob(job2.ID, "codex", "", "output2")
	db.MarkReviewAddressedByJobID(job2.ID, true)

	t.Run("addressed=false", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/jobs?addressed=false", nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &result)
		if len(result.Jobs) != 1 {
			t.Errorf("Expected 1 unaddressed job, got %d", len(result.Jobs))
		}
	})

	t.Run("branch filter", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/jobs?branch=main", nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &result)
		if len(result.Jobs) != 2 {
			t.Errorf("Expected 2 jobs on main, got %d", len(result.Jobs))
		}
	})
}

func TestHandleEnqueueExcludedBranch(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	repoDir := filepath.Join(tmpDir, "testrepo")
	testutil.InitTestGitRepo(t, repoDir)

	// Switch to excluded branch
	checkoutCmd := exec.Command("git", "-C", repoDir, "checkout", "-b", "wip-feature")
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout failed: %v\n%s", err, out)
	}

	// Create .roborev.toml with excluded_branches
	repoConfig := filepath.Join(repoDir, ".roborev.toml")
	configContent := `excluded_branches = ["wip-feature", "draft"]`
	if err := os.WriteFile(repoConfig, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write repo config: %v", err)
	}

	t.Run("enqueue on excluded branch returns skipped", func(t *testing.T) {
		reqData := EnqueueRequest{RepoPath: repoDir, GitRef: "HEAD", Agent: "test"}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200 for skipped enqueue, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Skipped bool   `json:"skipped"`
			Reason  string `json:"reason"`
		}
		testutil.DecodeJSON(t, w, &response)

		if !response.Skipped {
			t.Error("Expected skipped=true")
		}
		if !strings.Contains(response.Reason, "wip-feature") {
			t.Errorf("Expected reason to mention branch name, got %q", response.Reason)
		}

		// Verify no job was created
		queued, _, _, _, _, _, _, _ := db.GetJobCounts()
		if queued != 0 {
			t.Errorf("Expected 0 queued jobs, got %d", queued)
		}
	})

	t.Run("enqueue on non-excluded branch succeeds", func(t *testing.T) {
		// Switch to a non-excluded branch
		checkoutCmd := exec.Command("git", "checkout", "-b", "feature-ok")
		checkoutCmd.Dir = repoDir
		if out, err := checkoutCmd.CombinedOutput(); err != nil {
			t.Fatalf("git checkout failed: %v\n%s", err, out)
		}

		reqData := EnqueueRequest{RepoPath: repoDir, GitRef: "HEAD", Agent: "test"}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Errorf("Expected status 201 for successful enqueue, got %d: %s", w.Code, w.Body.String())
		}

		// Verify job was created
		queued, _, _, _, _, _, _, _ := db.GetJobCounts()
		if queued != 1 {
			t.Errorf("Expected 1 queued job, got %d", queued)
		}
	})
}

func TestHandleEnqueueBranchFallback(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	repoDir := filepath.Join(tmpDir, "testrepo")
	testutil.InitTestGitRepo(t, repoDir)

	// Switch to a named branch
	branchCmd := exec.Command("git", "-C", repoDir, "checkout", "-b", "my-feature")
	if out, err := branchCmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout failed: %v\n%s", err, out)
	}

	// Enqueue with empty branch field
	reqData := EnqueueRequest{
		RepoPath: repoDir,
		GitRef:   "HEAD",
		Agent:    "test",
	}
	req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
	w := httptest.NewRecorder()
	server.handleEnqueue(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var respJob storage.ReviewJob
	testutil.DecodeJSON(t, w, &respJob)

	// Verify the job has the detected branch, not empty
	job, err := db.GetJobByID(respJob.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Branch != "my-feature" {
		t.Errorf("expected branch %q, got %q", "my-feature", job.Branch)
	}
}

func TestHandleEnqueueBodySizeLimit(t *testing.T) {
	server, _, tmpDir := newTestServer(t)

	repoDir := filepath.Join(tmpDir, "testrepo")
	testutil.InitTestGitRepo(t, repoDir)

	t.Run("rejects oversized request body", func(t *testing.T) {
		// Create a request body larger than the default limit (200KB + 50KB overhead)
		largeDiff := strings.Repeat("a", 300*1024) // 300KB
		reqData := EnqueueRequest{
			RepoPath:    repoDir,
			GitRef:      "dirty",
			Agent:       "test",
			DiffContent: largeDiff,
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("Expected status 413, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Error string `json:"error"`
		}
		testutil.DecodeJSON(t, w, &response)

		if !strings.Contains(response.Error, "too large") {
			t.Errorf("Expected error about body size, got %q", response.Error)
		}
	})

	t.Run("rejects dirty review with empty diff_content", func(t *testing.T) {
		// git_ref="dirty" with empty diff_content should return a clear error
		reqData := EnqueueRequest{
			RepoPath: repoDir,
			GitRef:   "dirty",
			Agent:    "test",
			// diff_content intentionally omitted/empty
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected status 400, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Error string `json:"error"`
		}
		testutil.DecodeJSON(t, w, &response)

		if !strings.Contains(response.Error, "diff_content required") {
			t.Errorf("Expected error about diff_content required, got %q", response.Error)
		}
	})

	t.Run("accepts valid size dirty request", func(t *testing.T) {
		// Create a valid-sized diff (under 200KB)
		validDiff := strings.Repeat("a", 100*1024) // 100KB
		reqData := EnqueueRequest{
			RepoPath:    repoDir,
			GitRef:      "dirty",
			Agent:       "test",
			DiffContent: validDiff,
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
		}
	})
}

func TestHandleListJobsByID(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repos and jobs
	_, jobs := seedRepoWithJobs(t, db, filepath.Join(tmpDir, "testrepo"), 3, "repo1")
	job1ID := jobs[0].ID
	job2ID := jobs[1].ID
	job3ID := jobs[2].ID

	t.Run("fetches specific job by ID", func(t *testing.T) {
		// Request job 1 specifically
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/jobs?id=%d", job1ID), nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs    []storage.ReviewJob `json:"jobs"`
			HasMore bool                `json:"has_more"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 1 {
			t.Errorf("Expected exactly 1 job, got %d", len(response.Jobs))
		}
		if response.Jobs[0].ID != job1ID {
			t.Errorf("Expected job ID %d, got %d", job1ID, response.Jobs[0].ID)
		}
	})

	t.Run("fetches middle job correctly", func(t *testing.T) {
		// Request job 2 specifically (the middle job)
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/jobs?id=%d", job2ID), nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 1 {
			t.Errorf("Expected exactly 1 job, got %d", len(response.Jobs))
		}
		if response.Jobs[0].ID != job2ID {
			t.Errorf("Expected job ID %d, got %d", job2ID, response.Jobs[0].ID)
		}
	})

	t.Run("returns empty for non-existent job ID", func(t *testing.T) {
		// Request a job ID that doesn't exist
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?id=99999", nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Jobs) != 0 {
			t.Errorf("Expected 0 jobs for non-existent ID, got %d", len(response.Jobs))
		}
	})

	t.Run("returns error for invalid job ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs?id=notanumber", nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("without id param returns all jobs", func(t *testing.T) {
		// Request without id param should return all jobs
		req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var response struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &response)

		// Should have all 3 jobs
		if len(response.Jobs) != 3 {
			t.Errorf("Expected 3 jobs, got %d", len(response.Jobs))
		}

		// Verify all job IDs are present (order may vary due to same-second timestamps)
		foundIDs := make(map[int64]bool)
		for _, job := range response.Jobs {
			foundIDs[job.ID] = true
		}
		if !foundIDs[job1ID] || !foundIDs[job2ID] || !foundIDs[job3ID] {
			t.Errorf("Expected jobs %d, %d, %d but found %v", job1ID, job2ID, job3ID, foundIDs)
		}
	})
}

func TestHandleEnqueuePromptJob(t *testing.T) {
	repoDir := t.TempDir()
	testutil.InitTestGitRepo(t, repoDir)

	server, _, _ := newTestServer(t)

	t.Run("enqueues prompt job successfully", func(t *testing.T) {
		reqData := EnqueueRequest{
			RepoPath:     repoDir,
			GitRef:       "prompt",
			Agent:        "test",
			CustomPrompt: "Explain this codebase",
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var job storage.ReviewJob
		testutil.DecodeJSON(t, w, &job)

		if job.GitRef != "prompt" {
			t.Errorf("Expected git_ref 'prompt', got '%s'", job.GitRef)
		}
		if job.Agent != "test" {
			t.Errorf("Expected agent 'test', got '%s'", job.Agent)
		}
		if job.Status != storage.JobStatusQueued {
			t.Errorf("Expected status 'queued', got '%s'", job.Status)
		}
	})

	t.Run("git_ref prompt without custom_prompt is treated as branch review", func(t *testing.T) {
		// With no custom_prompt, git_ref="prompt" is treated as trying to review
		// a branch/commit named "prompt" (not a prompt job). This allows reviewing
		// branches literally named "prompt" without collision.
		reqData := EnqueueRequest{
			RepoPath: repoDir,
			GitRef:   "prompt",
			Agent:    "test",
			// no custom_prompt - should try to resolve "prompt" as a git ref
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		// Should fail because there's no branch named "prompt", not because
		// custom_prompt is missing
		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 (invalid commit), got %d: %s", w.Code, w.Body.String())
		}

		if strings.Contains(w.Body.String(), "custom_prompt required") {
			t.Errorf("Should NOT require custom_prompt for git_ref=prompt, got: %s", w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "invalid commit") {
			t.Errorf("Expected 'invalid commit' error, got: %s", w.Body.String())
		}
	})

	t.Run("prompt job with reasoning level", func(t *testing.T) {
		reqData := EnqueueRequest{
			RepoPath:     repoDir,
			GitRef:       "prompt",
			Agent:        "test",
			Reasoning:    "fast",
			CustomPrompt: "Quick analysis",
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var job storage.ReviewJob
		testutil.DecodeJSON(t, w, &job)

		if job.Reasoning != "fast" {
			t.Errorf("Expected reasoning 'fast', got '%s'", job.Reasoning)
		}
	})

	t.Run("prompt job with agentic flag", func(t *testing.T) {
		reqData := EnqueueRequest{
			RepoPath:     repoDir,
			GitRef:       "prompt",
			Agent:        "test",
			CustomPrompt: "Fix all bugs",
			Agentic:      true,
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var job storage.ReviewJob
		testutil.DecodeJSON(t, w, &job)

		if !job.Agentic {
			t.Error("Expected Agentic to be true")
		}
	})

	t.Run("prompt job without agentic defaults to false", func(t *testing.T) {
		reqData := EnqueueRequest{
			RepoPath:     repoDir,
			GitRef:       "prompt",
			Agent:        "test",
			CustomPrompt: "Read-only review",
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()

		server.handleEnqueue(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var job storage.ReviewJob
		testutil.DecodeJSON(t, w, &job)

		if job.Agentic {
			t.Error("Expected Agentic to be false by default")
		}
	})
}

func TestHandleEnqueueAgentAvailability(t *testing.T) {
	// Shared read-only git repo created once (all subtests use different servers for DB isolation)
	repoDir := filepath.Join(t.TempDir(), "repo")
	testutil.InitTestGitRepo(t, repoDir)
	headSHA := testutil.GetHeadSHA(t, repoDir)

	// Create an isolated dir containing only a wrapper for git.
	// We can't just use git's parent dir because it may contain real agent
	// binaries (e.g. codex, claude) that would defeat the PATH isolation.
	// Symlinks don't work reliably on Windows, so we use wrapper scripts.
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("git not found in PATH")
	}
	gitOnlyDir := t.TempDir()
	if runtime.GOOS == "windows" {
		wrapper := fmt.Sprintf("@\"%s\" %%*\r\n", gitPath)
		if err := os.WriteFile(filepath.Join(gitOnlyDir, "git.cmd"), []byte(wrapper), 0755); err != nil {
			t.Fatal(err)
		}
	} else {
		wrapper := fmt.Sprintf("#!/bin/sh\nexec '%s' \"$@\"\n", gitPath)
		if err := os.WriteFile(filepath.Join(gitOnlyDir, "git"), []byte(wrapper), 0755); err != nil {
			t.Fatal(err)
		}
	}

	mockScript := "#!/bin/sh\nexit 0\n"

	tests := []struct {
		name          string
		requestAgent  string
		mockBinaries  []string // binary names to place in PATH
		expectedAgent string   // expected agent stored in job
		expectedCode  int      // expected HTTP status code
	}{
		{
			name:          "explicit test agent preserved",
			requestAgent:  "test",
			mockBinaries:  nil,
			expectedAgent: "test",
			expectedCode:  http.StatusCreated,
		},
		{
			name:          "unavailable codex falls back to claude-code",
			requestAgent:  "codex",
			mockBinaries:  []string{"claude"},
			expectedAgent: "claude-code",
			expectedCode:  http.StatusCreated,
		},
		{
			name:          "default agent falls back when codex not installed",
			requestAgent:  "",
			mockBinaries:  []string{"claude"},
			expectedAgent: "claude-code",
			expectedCode:  http.StatusCreated,
		},
		{
			name:          "explicit codex kept when available",
			requestAgent:  "codex",
			mockBinaries:  []string{"codex"},
			expectedAgent: "codex",
			expectedCode:  http.StatusCreated,
		},
		{
			name:          "default falls back to kilo when only kilo available",
			requestAgent:  "",
			mockBinaries:  []string{"kilo"},
			expectedAgent: "kilo",
			expectedCode:  http.StatusCreated,
		},
		{
			name:         "no agents available returns 503",
			requestAgent: "codex",
			mockBinaries: nil,
			expectedCode: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Each subtest gets its own server/DB to avoid SHA dedup conflicts
			server, _, _ := newTestServer(t)

			// Isolate PATH: only mock binaries + git (no real agent CLIs)
			origPath := os.Getenv("PATH")
			mockDir := t.TempDir()
			for _, bin := range tt.mockBinaries {
				name := bin
				content := mockScript
				if runtime.GOOS == "windows" {
					name = bin + ".cmd"
					content = "@exit /b 0\r\n"
				}
				if err := os.WriteFile(filepath.Join(mockDir, name), []byte(content), 0755); err != nil {
					t.Fatal(err)
				}
			}
			os.Setenv("PATH", mockDir+string(os.PathListSeparator)+gitOnlyDir)
			t.Cleanup(func() { os.Setenv("PATH", origPath) })

			reqData := EnqueueRequest{
				RepoPath:  repoDir,
				CommitSHA: headSHA,
			}
			if tt.requestAgent != "" {
				reqData.Agent = tt.requestAgent
			}
			req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
			w := httptest.NewRecorder()

			server.handleEnqueue(w, req)

			if w.Code != tt.expectedCode {
				t.Fatalf("Expected status %d, got %d: %s", tt.expectedCode, w.Code, w.Body.String())
			}

			if tt.expectedCode != http.StatusCreated {
				return
			}

			var job storage.ReviewJob
			testutil.DecodeJSON(t, w, &job)

			if job.Agent != tt.expectedAgent {
				t.Errorf("Expected agent %q, got %q", tt.expectedAgent, job.Agent)
			}
		})
	}
}

func TestHandleEnqueueWorktreeGitDirIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping worktree test on Windows due to path differences")
	}

	tmpDir := t.TempDir()

	// Create main repo with initial commit (commit A)
	mainRepo := filepath.Join(tmpDir, "main-repo")
	testutil.InitTestGitRepo(t, mainRepo)
	commitA := testutil.GetHeadSHA(t, mainRepo)

	// Create a worktree
	worktreeDir := filepath.Join(tmpDir, "worktree")
	wtCmd := exec.Command("git", "-C", mainRepo, "worktree", "add", "-b", "wt-branch", worktreeDir)
	if out, err := wtCmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v\n%s", err, out)
	}

	// Make a new commit in the worktree so HEAD differs (commit B)
	wtFile := filepath.Join(worktreeDir, "worktree-file.txt")
	if err := os.WriteFile(wtFile, []byte("worktree content"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	for _, args := range [][]string{
		{"git", "-C", worktreeDir, "add", "."},
		{"git", "-C", worktreeDir, "commit", "-m", "worktree commit"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	commitB := testutil.GetHeadSHA(t, worktreeDir)

	if commitA == commitB {
		t.Fatal("test setup error: commits A and B should differ")
	}

	enqueue := func(t *testing.T) storage.ReviewJob {
		t.Helper()
		server, _, _ := newTestServer(t)
		reqData := EnqueueRequest{
			RepoPath: worktreeDir,
			GitRef:   "HEAD",
			Agent:    "test",
		}
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
		w := httptest.NewRecorder()
		server.handleEnqueue(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var job storage.ReviewJob
		testutil.DecodeJSON(t, w, &job)
		return job
	}

	t.Run("leaked GIT_DIR resolves wrong commit", func(t *testing.T) {
		// Set GIT_DIR to the main repo's .git dir, simulating a post-commit hook.
		// t.Setenv restores the original value after the subtest.
		mainGitDir := filepath.Join(mainRepo, ".git")
		t.Setenv("GIT_DIR", mainGitDir)

		job := enqueue(t)

		// With GIT_DIR leaked, git resolves HEAD from the main repo (commit A)
		// instead of the worktree (commit B). This is the bug.
		if job.GitRef != commitA {
			t.Errorf("expected leaked GIT_DIR to resolve commit A (%s), got %s", commitA, job.GitRef)
		}
	})

	t.Run("cleared GIT_DIR resolves correct commit", func(t *testing.T) {
		// Simulate the daemon startup fix: clear GIT_DIR before handling requests.
		// This is what daemonRunCmd() does with os.Unsetenv.
		t.Setenv("GIT_DIR", "")
		os.Unsetenv("GIT_DIR")

		job := enqueue(t)

		// Without GIT_DIR, git uses cmd.Dir correctly and resolves the worktree's HEAD.
		if job.GitRef != commitB {
			t.Errorf("expected worktree commit B (%s), got %s", commitB, job.GitRef)
		}
	})
}

// TestHandleEnqueueRangeFromRootCommit verifies that a range review starting
// from the root commit (which has no parent) succeeds by falling back to the
// empty tree SHA.
func TestHandleEnqueueRangeFromRootCommit(t *testing.T) {
	repoDir := t.TempDir()
	testutil.InitTestGitRepo(t, repoDir)

	// Get the root commit SHA
	rootSHA, err := gitpkg.ResolveSHA(repoDir, "HEAD")
	if err != nil {
		t.Fatalf("resolve root SHA: %v", err)
	}

	// Add a second commit so we have a range
	testFile := filepath.Join(repoDir, "second.txt")
	if err := os.WriteFile(testFile, []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "-C", repoDir, "add", "."},
		{"git", "-C", repoDir, "commit", "-m", "second"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	endSHA, err := gitpkg.ResolveSHA(repoDir, "HEAD")
	if err != nil {
		t.Fatalf("resolve end SHA: %v", err)
	}

	server, _, _ := newTestServer(t)

	// Send range starting from root commit's parent (rootSHA^..endSHA)
	// This is what the CLI sends for "roborev review <root> <end>"
	rangeRef := rootSHA + "^.." + endSHA
	reqData := EnqueueRequest{
		RepoPath: repoDir,
		GitRef:   rangeRef,
		Agent:    "test",
	}
	req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
	w := httptest.NewRecorder()

	server.handleEnqueue(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var job storage.ReviewJob
	testutil.DecodeJSON(t, w, &job)

	// The stored range should use the empty tree SHA as the start
	expectedRef := gitpkg.EmptyTreeSHA + ".." + endSHA
	if job.GitRef != expectedRef {
		t.Errorf("expected git_ref %q, got %q", expectedRef, job.GitRef)
	}
}

// TestHandleEnqueueRangeNonCommitObjectRejects verifies that the root-commit
// fallback does not trigger for non-commit objects (e.g. blobs).
func TestHandleEnqueueRangeNonCommitObjectRejects(t *testing.T) {
	repoDir := t.TempDir()
	testutil.InitTestGitRepo(t, repoDir)

	endSHA, err := gitpkg.ResolveSHA(repoDir, "HEAD")
	if err != nil {
		t.Fatalf("resolve HEAD: %v", err)
	}

	// Get a blob SHA (the test.txt file created by InitTestGitRepo)
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD:test.txt")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("get blob SHA: %v", err)
	}
	blobSHA := strings.TrimSpace(string(out))

	server, _, _ := newTestServer(t)

	// A blob^ should not fall back to EmptyTreeSHA — it should return 400
	rangeRef := blobSHA + "^.." + endSHA
	reqData := EnqueueRequest{
		RepoPath: repoDir,
		GitRef:   rangeRef,
		Agent:    "test",
	}
	req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/enqueue", reqData)
	w := httptest.NewRecorder()

	server.handleEnqueue(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid start commit") {
		t.Errorf("expected 'invalid start commit' error, got: %s", w.Body.String())
	}
}

func TestHandleListJobsIDParsing(t *testing.T) {
	server, _, _ := newTestServer(t)
	testInvalidIDParsing(t, server.handleListJobs, "/api/jobs?id=%s")
}

func TestHandleListJobsJobTypeFilter(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	repoDir := filepath.Join(tmpDir, "repo-jt")
	testutil.InitTestGitRepo(t, repoDir)
	repo, _ := db.GetOrCreateRepo(repoDir)
	commit, _ := db.GetOrCreateCommit(
		repo.ID, "jt-abc", "Author", "Subject", time.Now(),
	)

	// Create a review job
	reviewJob, _ := db.EnqueueJob(storage.EnqueueOpts{
		RepoID:   repo.ID,
		CommitID: commit.ID,
		GitRef:   "jt-abc",
		Agent:    "test",
	})

	// Create a fix job parented to the review
	db.EnqueueJob(storage.EnqueueOpts{
		RepoID:      repo.ID,
		CommitID:    commit.ID,
		GitRef:      "jt-abc",
		Agent:       "test",
		JobType:     storage.JobTypeFix,
		ParentJobID: reviewJob.ID,
	})

	t.Run("job_type=fix returns only fix jobs", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet, "/api/jobs?job_type=fix", nil,
		)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &resp)

		if len(resp.Jobs) != 1 {
			t.Fatalf("Expected 1 fix job, got %d", len(resp.Jobs))
		}
		if resp.Jobs[0].JobType != storage.JobTypeFix {
			t.Errorf(
				"Expected job_type 'fix', got %q", resp.Jobs[0].JobType,
			)
		}
	})

	t.Run("no job_type returns all jobs", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet, "/api/jobs", nil,
		)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &resp)

		if len(resp.Jobs) != 2 {
			t.Errorf("Expected 2 jobs total, got %d", len(resp.Jobs))
		}
	})

	t.Run("exclude_job_type=fix returns only non-fix jobs", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet, "/api/jobs?exclude_job_type=fix", nil,
		)
		w := httptest.NewRecorder()
		server.handleListJobs(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		testutil.DecodeJSON(t, w, &resp)

		if len(resp.Jobs) != 1 {
			t.Fatalf("Expected 1 non-fix job, got %d", len(resp.Jobs))
		}
		if resp.Jobs[0].JobType == storage.JobTypeFix {
			t.Error("Expected non-fix job, got fix")
		}
	})
}
