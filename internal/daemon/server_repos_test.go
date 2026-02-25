package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

func TestHandleListRepos(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	t.Run("empty database", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
		w := httptest.NewRecorder()

		server.handleListRepos(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response struct {
			Repos      []struct{ Name string } `json:"repos"`
			TotalCount int                     `json:"total_count"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Repos) != 0 {
			t.Errorf("Expected 0 repos, got %d", len(response.Repos))
		}
		if response.TotalCount != 0 {
			t.Errorf("Expected total_count 0, got %d", response.TotalCount)
		}
	})

	// Create repos and jobs
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo1"), 3, "repo1")
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo2"), 2, "repo2")

	t.Run("repos with jobs", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
		w := httptest.NewRecorder()

		server.handleListRepos(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response struct {
			Repos []struct {
				Name  string `json:"name"`
				Count int    `json:"count"`
			} `json:"repos"`
			TotalCount int `json:"total_count"`
		}
		testutil.DecodeJSON(t, w, &response)

		if len(response.Repos) != 2 {
			t.Errorf("Expected 2 repos, got %d", len(response.Repos))
		}
		if response.TotalCount != 5 {
			t.Errorf("Expected total_count 5, got %d", response.TotalCount)
		}

		repoMap := make(map[string]int)
		for _, r := range response.Repos {
			repoMap[r.Name] = r.Count
		}
		if repoMap["repo1"] != 3 {
			t.Errorf("Expected repo1 count 3, got %d", repoMap["repo1"])
		}
		if repoMap["repo2"] != 2 {
			t.Errorf("Expected repo2 count 2, got %d", repoMap["repo2"])
		}
	})

	t.Run("wrong method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/repos", nil)
		w := httptest.NewRecorder()

		server.handleListRepos(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected status 405 for POST, got %d", w.Code)
		}
	})
}

func TestHandleListReposWithBranchFilter(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repos and jobs
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo1"), 3, "repo1")
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo2"), 2, "repo2")

	// Set branches: repo1 jobs 1,2 = main, job 3 = feature; repo2 jobs 4,5 = main
	setJobBranch(t, db, 1, "main")
	setJobBranch(t, db, 2, "main")
	setJobBranch(t, db, 4, "main")
	setJobBranch(t, db, 5, "main")
	setJobBranch(t, db, 3, "feature")

	type reposResponse struct {
		Repos      []struct{ Name string } `json:"repos"`
		TotalCount int                     `json:"total_count"`
	}

	t.Run("filter by main branch", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos?branch=main", nil)
		w := httptest.NewRecorder()
		server.handleListRepos(w, req)
		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response reposResponse
		testutil.DecodeJSON(t, w, &response)
		if len(response.Repos) != 2 {
			t.Errorf("Expected 2 repos with main branch, got %d", len(response.Repos))
		}
		if response.TotalCount != 4 {
			t.Errorf("Expected total_count 4, got %d", response.TotalCount)
		}
	})

	t.Run("filter by feature branch", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos?branch=feature", nil)
		w := httptest.NewRecorder()
		server.handleListRepos(w, req)
		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response reposResponse
		testutil.DecodeJSON(t, w, &response)
		if len(response.Repos) != 1 {
			t.Errorf("Expected 1 repo with feature branch, got %d", len(response.Repos))
		}
		if response.TotalCount != 1 {
			t.Errorf("Expected total_count 1, got %d", response.TotalCount)
		}
	})

	t.Run("nonexistent branch returns empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos?branch=nonexistent", nil)
		w := httptest.NewRecorder()
		server.handleListRepos(w, req)
		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response reposResponse
		testutil.DecodeJSON(t, w, &response)
		if response.TotalCount != 0 {
			t.Errorf("Expected total_count 0 for nonexistent branch, got %d", response.TotalCount)
		}
	})
}

func TestHandleListBranches(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repos and jobs
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo1"), 3, "repo1")
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo2"), 2, "repo2")

	// Set branches: jobs 1,2,4 = main, job 3 = feature, job 5 = no branch
	setJobBranch(t, db, 1, "main")
	setJobBranch(t, db, 2, "main")
	setJobBranch(t, db, 4, "main")
	setJobBranch(t, db, 3, "feature")
	// job 5 left empty

	type branchesResponse struct {
		Branches       []json.RawMessage `json:"branches"`
		TotalCount     int               `json:"total_count"`
		NullsRemaining int               `json:"nulls_remaining"`
	}

	t.Run("list all branches", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/branches", nil)
		w := httptest.NewRecorder()
		server.handleListBranches(w, req)
		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response branchesResponse
		testutil.DecodeJSON(t, w, &response)
		if len(response.Branches) != 3 {
			t.Errorf("Expected 3 branches, got %d", len(response.Branches))
		}
		if response.TotalCount != 5 {
			t.Errorf("Expected total_count 5, got %d", response.TotalCount)
		}
		if response.NullsRemaining != 1 {
			t.Errorf("Expected nulls_remaining 1, got %d", response.NullsRemaining)
		}
	})

	t.Run("filter by repo", func(t *testing.T) {
		repoPath := filepath.Join(tmpDir, "repo1")
		req := httptest.NewRequest(http.MethodGet, "/api/branches?repo="+repoPath, nil)
		w := httptest.NewRecorder()
		server.handleListBranches(w, req)
		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response branchesResponse
		testutil.DecodeJSON(t, w, &response)
		if len(response.Branches) != 2 {
			t.Errorf("Expected 2 branches for repo1, got %d", len(response.Branches))
		}
		if response.TotalCount != 3 {
			t.Errorf("Expected total_count 3 for repo1, got %d", response.TotalCount)
		}
	})

	t.Run("filter by multiple repos", func(t *testing.T) {
		repo1Path := filepath.Join(tmpDir, "repo1")
		repo2Path := filepath.Join(tmpDir, "repo2")
		req := httptest.NewRequest(http.MethodGet, "/api/branches?repo="+repo1Path+"&repo="+repo2Path, nil)
		w := httptest.NewRecorder()
		server.handleListBranches(w, req)
		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response branchesResponse
		testutil.DecodeJSON(t, w, &response)
		if len(response.Branches) != 3 {
			t.Errorf("Expected 3 branches for both repos, got %d", len(response.Branches))
		}
		if response.TotalCount != 5 {
			t.Errorf("Expected total_count 5 for both repos, got %d", response.TotalCount)
		}
	})

	t.Run("empty repo param treated as no filter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/branches?repo=", nil)
		w := httptest.NewRecorder()
		server.handleListBranches(w, req)
		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response branchesResponse
		testutil.DecodeJSON(t, w, &response)
		if len(response.Branches) != 3 {
			t.Errorf("Expected 3 branches (empty repo = no filter), got %d", len(response.Branches))
		}
		if response.TotalCount != 5 {
			t.Errorf("Expected total_count 5 (empty repo = no filter), got %d", response.TotalCount)
		}
	})

	t.Run("wrong method fails", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/branches", nil)
		w := httptest.NewRecorder()

		server.handleListBranches(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected status 405 for POST, got %d", w.Code)
		}
	})
}

func TestHandleRegisterRepo(t *testing.T) {
	t.Run("GET returns 405", func(t *testing.T) {
		server, _, _ := newTestServer(t)
		req := httptest.NewRequest(http.MethodGet, "/api/repos/register", nil)
		w := httptest.NewRecorder()
		server.handleRegisterRepo(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected 405, got %d", w.Code)
		}
	})

	t.Run("empty body returns 400", func(t *testing.T) {
		server, _, _ := newTestServer(t)
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/repos/register", map[string]string{})
		w := httptest.NewRecorder()
		server.handleRegisterRepo(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("non-git path returns 400", func(t *testing.T) {
		server, _, _ := newTestServer(t)
		plainDir := t.TempDir()
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/repos/register", map[string]string{
			"repo_path": plainDir,
		})
		w := httptest.NewRecorder()
		server.handleRegisterRepo(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("valid git repo returns 200 and persists", func(t *testing.T) {
		server, db, tmpDir := newTestServer(t)
		repoDir := filepath.Join(tmpDir, "testrepo")
		testutil.InitTestGitRepo(t, repoDir)

		// Add a remote so identity resolves to something meaningful
		remoteCmd := exec.Command("git", "-C", repoDir, "remote", "add", "origin", "https://github.com/test/testrepo.git")
		if out, err := remoteCmd.CombinedOutput(); err != nil {
			t.Fatalf("git remote add failed: %v\n%s", err, out)
		}

		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/repos/register", map[string]string{
			"repo_path": repoDir,
		})
		w := httptest.NewRecorder()
		server.handleRegisterRepo(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var repo storage.Repo
		testutil.DecodeJSON(t, w, &repo)
		if repo.ID == 0 {
			t.Error("Expected non-zero repo ID")
		}
		if repo.Identity == "" {
			t.Error("Expected non-empty identity")
		}

		// Verify repo is in the DB
		repos, err := db.ListRepos()
		if err != nil {
			t.Fatalf("ListRepos: %v", err)
		}
		if len(repos) != 1 {
			t.Fatalf("Expected 1 repo in DB, got %d", len(repos))
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		server, db, tmpDir := newTestServer(t)
		repoDir := filepath.Join(tmpDir, "testrepo")
		testutil.InitTestGitRepo(t, repoDir)

		body := map[string]string{"repo_path": repoDir}

		// First call
		req1 := testutil.MakeJSONRequest(t, http.MethodPost, "/api/repos/register", body)
		w1 := httptest.NewRecorder()
		server.handleRegisterRepo(w1, req1)
		if w1.Code != http.StatusOK {
			t.Fatalf("First call: expected 200, got %d: %s", w1.Code, w1.Body.String())
		}

		// Second call
		req2 := testutil.MakeJSONRequest(t, http.MethodPost, "/api/repos/register", body)
		w2 := httptest.NewRecorder()
		server.handleRegisterRepo(w2, req2)
		if w2.Code != http.StatusOK {
			t.Fatalf("Second call: expected 200, got %d: %s", w2.Code, w2.Body.String())
		}

		// Still only one repo in DB
		repos, err := db.ListRepos()
		if err != nil {
			t.Fatalf("ListRepos: %v", err)
		}
		if len(repos) != 1 {
			t.Fatalf("Expected 1 repo in DB after two calls, got %d", len(repos))
		}
	})
}
