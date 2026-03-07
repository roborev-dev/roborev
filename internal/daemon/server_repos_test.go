package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

type repoListResponse struct {
	Repos []struct {
		Name     string `json:"name"`
		RootPath string `json:"root_path"`
		Count    int    `json:"count"`
	} `json:"repos"`
	TotalCount int `json:"total_count"`
}

func TestHandleListRepos(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	t.Run("empty database", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
		w := httptest.NewRecorder()

		server.handleListRepos(w, req)

		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response repoListResponse
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

		var response repoListResponse
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

		testutil.AssertStatusCode(t, w, http.StatusMethodNotAllowed)
	})
}

func TestHandleListReposWithBranchFilter(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repos and jobs
	_, repo1Jobs := seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo1"), 3, "repo1")
	_, repo2Jobs := seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo2"), 2, "repo2")

	// Set branches: repo1 jobs 0,1 = main, job 2 = feature; repo2 jobs 0,1 = main
	setJobBranch(t, db, repo1Jobs[0].ID, "main")
	setJobBranch(t, db, repo1Jobs[1].ID, "main")
	setJobBranch(t, db, repo1Jobs[2].ID, "feature")
	setJobBranch(t, db, repo2Jobs[0].ID, "main")
	setJobBranch(t, db, repo2Jobs[1].ID, "main")

	t.Run("filter by main branch", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/repos?branch=main", nil)
		w := httptest.NewRecorder()
		server.handleListRepos(w, req)
		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response repoListResponse
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

		var response repoListResponse
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

		var response repoListResponse
		testutil.DecodeJSON(t, w, &response)
		if response.TotalCount != 0 {
			t.Errorf("Expected total_count 0 for nonexistent branch, got %d", response.TotalCount)
		}
	})
}

func TestHandleListReposWithPrefixFilter(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repos under a "workspace" parent and one outside it
	workspace := filepath.Join(tmpDir, "workspace")
	_, repo1Jobs := seedRepoWithJobs(t, db, filepath.Join(workspace, "repo1"), 3, "repo1")
	_, repo2Jobs := seedRepoWithJobs(t, db, filepath.Join(workspace, "repo2"), 2, "repo2")
	seedRepoWithJobs(t, db, filepath.Join(tmpDir, "other-repo"), 1, "other")

	// Set branches on workspace repos: repo1 jobs 0,1=main, 2=feature; repo2 jobs 0,1=main
	setJobBranch(t, db, repo1Jobs[0].ID, "main")
	setJobBranch(t, db, repo1Jobs[1].ID, "main")
	setJobBranch(t, db, repo1Jobs[2].ID, "feature")
	setJobBranch(t, db, repo2Jobs[0].ID, "main")
	setJobBranch(t, db, repo2Jobs[1].ID, "main")

	tests := []struct {
		name          string
		query         string
		expectedCount int
		expectedTotal int
	}{
		{"prefix returns only child repos", "?prefix=" + url.QueryEscape(workspace), 2, 5},
		{"prefix excludes non-matching repos", "?prefix=" + url.QueryEscape(filepath.Join(tmpDir, "nonexistent")), 0, 0},
		{"prefix + branch combined filter", "?prefix=" + url.QueryEscape(workspace) + "&branch=main", 2, 4},
		{"prefix + feature branch narrows results", "?prefix=" + url.QueryEscape(workspace) + "&branch=feature", 1, 1},
		{"prefix with trailing slash is normalized", "?prefix=" + url.QueryEscape(workspace+"/"), 2, 5},
		{"prefix with dot-dot is normalized", "?prefix=" + url.QueryEscape(workspace+"/../"+filepath.Base(workspace)), 2, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/repos"+tt.query, nil)
			w := httptest.NewRecorder()
			server.handleListRepos(w, req)
			testutil.AssertStatusCode(t, w, http.StatusOK)

			var response repoListResponse
			testutil.DecodeJSON(t, w, &response)
			if len(response.Repos) != tt.expectedCount {
				t.Errorf("Expected %d repos, got %d", tt.expectedCount, len(response.Repos))
			}
			if response.TotalCount != tt.expectedTotal {
				t.Errorf("Expected total_count %d, got %d", tt.expectedTotal, response.TotalCount)
			}
		})
	}
}

func TestHandleListReposSlashNormalization(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Store repos with forward-slash paths (matching ToSlash output)
	ws := filepath.ToSlash(tmpDir) + "/slash-ws"
	seedRepoWithJobs(t, db, ws+"/repo-x", 2, "rx")
	seedRepoWithJobs(t, db, ws+"/repo-y", 1, "ry")
	seedRepoWithJobs(t, db, filepath.ToSlash(tmpDir)+"/other-z", 1, "rz")

	t.Run("forward-slash prefix matches stored paths", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet,
			"/api/repos?prefix="+url.QueryEscape(ws), nil)
		w := httptest.NewRecorder()
		server.handleListRepos(w, req)
		testutil.AssertStatusCode(t, w, http.StatusOK)

		var response repoListResponse
		testutil.DecodeJSON(t, w, &response)
		if len(response.Repos) != 2 {
			t.Errorf(
				"Expected 2 repos with forward-slash prefix, got %d",
				len(response.Repos),
			)
		}
		if response.TotalCount != 3 {
			t.Errorf("Expected total_count 3, got %d", response.TotalCount)
		}
		names := make(map[string]bool)
		for _, r := range response.Repos {
			names[r.Name] = true
		}
		if !names["repo-x"] || !names["repo-y"] {
			t.Errorf("Expected repo-x and repo-y, got %v", response.Repos)
		}
		if names["other-z"] {
			t.Error("other-z should not be in prefix-filtered results")
		}
	})
}

func TestHandleListBranches(t *testing.T) {
	server, db, tmpDir := newTestServer(t)

	// Create repos and jobs
	_, repo1Jobs := seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo1"), 3, "repo1")
	_, repo2Jobs := seedRepoWithJobs(t, db, filepath.Join(tmpDir, "repo2"), 2, "repo2")

	// Set branches: jobs 0,1 for repo1 = main, job 2 = feature, job 0 for repo2 = main, job 1 = no branch
	setJobBranch(t, db, repo1Jobs[0].ID, "main")
	setJobBranch(t, db, repo1Jobs[1].ID, "main")
	setJobBranch(t, db, repo1Jobs[2].ID, "feature")
	setJobBranch(t, db, repo2Jobs[0].ID, "main")
	// repo2Jobs[1].ID left empty

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

		testutil.AssertStatusCode(t, w, http.StatusMethodNotAllowed)
	})
}

func TestHandleRegisterRepo(t *testing.T) {
	t.Run("GET returns 405", func(t *testing.T) {
		server, _, _ := newTestServer(t)
		req := httptest.NewRequest(http.MethodGet, "/api/repos/register", nil)
		w := httptest.NewRecorder()
		server.handleRegisterRepo(w, req)
		testutil.AssertStatusCode(t, w, http.StatusMethodNotAllowed)
	})

	t.Run("empty body returns 400", func(t *testing.T) {
		server, _, _ := newTestServer(t)
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/repos/register", map[string]string{})
		w := httptest.NewRecorder()
		server.handleRegisterRepo(w, req)
		testutil.AssertStatusCode(t, w, http.StatusBadRequest)
	})

	t.Run("non-git path returns 400", func(t *testing.T) {
		server, _, _ := newTestServer(t)
		plainDir := t.TempDir()
		req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/repos/register", map[string]string{
			"repo_path": plainDir,
		})
		w := httptest.NewRecorder()
		server.handleRegisterRepo(w, req)
		testutil.AssertStatusCode(t, w, http.StatusBadRequest)
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

		testutil.AssertStatusCode(t, w, http.StatusOK)

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
		testutil.AssertStatusCode(t, w1, http.StatusOK)

		// Second call
		req2 := testutil.MakeJSONRequest(t, http.MethodPost, "/api/repos/register", body)
		w2 := httptest.NewRecorder()
		server.handleRegisterRepo(w2, req2)
		testutil.AssertStatusCode(t, w2, http.StatusOK)

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
