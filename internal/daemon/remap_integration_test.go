//go:build integration

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitpkg "github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

// gitHelper runs git commands in a repo directory.
type gitHelper struct {
	t   *testing.T
	dir string
}

func (g *gitHelper) run(args ...string) {
	g.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = g.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		g.t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

func (g *gitHelper) headSHA() string {
	g.t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = g.dir
	out, err := cmd.Output()
	if err != nil {
		g.t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func (g *gitHelper) commitFile(name, content, msg string) {
	g.t.Helper()
	if err := os.WriteFile(filepath.Join(g.dir, name), []byte(content), 0644); err != nil {
		g.t.Fatal(err)
	}
	g.run("add", name)
	g.run("commit", "-m", msg)
}

func newGitRepo(t *testing.T) *gitHelper {
	t.Helper()
	dir := t.TempDir()
	g := &gitHelper{t: t, dir: dir}
	g.run("init", "-b", "main")
	g.run("config", "user.email", "test@test.com")
	g.run("config", "user.name", "Test")
	return g
}

// TestRemapAfterRebase exercises the rebase remap flow by calling
// the HTTP handler directly with real git operations. This validates
// the full DB flow (patch-id matching, commit creation, git_ref
// update) without requiring a running daemon. End-to-end testing
// through the actual post-rewrite hook → CLI → daemon path is
// covered by manual testing (see design doc verification section).
func TestRemapAfterRebase(t *testing.T) {
	server, db, _ := newTestServer(t)

	repo := newGitRepo(t)
	repo.commitFile("base.txt", "base content", "initial commit")

	// Resolve symlinks for macOS /var -> /private/var
	repoDir, err := filepath.EvalSymlinks(repo.dir)
	if err != nil {
		repoDir = repo.dir
	}

	dbRepo, err := db.GetOrCreateRepo(repoDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	// Create feature branch with a commit
	repo.run("checkout", "-b", "feature")
	repo.commitFile("feature.txt", "feature content", "add feature")
	oldSHA := repo.headSHA()

	patchID := gitpkg.GetPatchID(repo.dir, oldSHA)
	if patchID == "" {
		t.Fatal("expected non-empty patch-id for feature commit")
	}

	// Enqueue and complete a review
	commit, err := db.GetOrCreateCommit(
		dbRepo.ID, oldSHA, "Test", "add feature", time.Now(),
	)
	if err != nil {
		t.Fatalf("GetOrCreateCommit: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{
		RepoID:   dbRepo.ID,
		CommitID: commit.ID,
		GitRef:   oldSHA,
		Agent:    "test",
		PatchID:  patchID,
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	_, err = db.ClaimJob("worker-int")
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	err = db.CompleteJob(job.ID, "test", "prompt", "LGTM - no issues found")
	if err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}

	// Advance main so rebase has work to do
	repo.run("checkout", "main")
	repo.commitFile("main2.txt", "more main", "advance main")

	// Rebase feature onto main
	repo.run("checkout", "feature")
	repo.run("rebase", "main")
	newSHA := repo.headSHA()

	if oldSHA == newSHA {
		t.Fatal("SHAs should differ after rebase")
	}

	newPatchID := gitpkg.GetPatchID(repo.dir, newSHA)
	if patchID != newPatchID {
		t.Fatalf("patch-ids should match after clean rebase: %s != %s",
			patchID, newPatchID)
	}

	info, err := gitpkg.GetCommitInfo(repo.dir, newSHA)
	if err != nil {
		t.Fatalf("GetCommitInfo: %v", err)
	}

	// POST /api/remap
	reqData := RemapRequest{
		RepoPath: repoDir,
		Mappings: []RemapMapping{
			{
				OldSHA:    oldSHA,
				NewSHA:    newSHA,
				PatchID:   patchID,
				Author:    info.Author,
				Subject:   info.Subject,
				Timestamp: info.Timestamp.Format(time.RFC3339),
			},
		},
	}
	req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/remap", reqData)
	w := httptest.NewRecorder()
	server.handleRemap(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]int
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["remapped"] != 1 {
		t.Errorf("expected remapped=1, got %d", result["remapped"])
	}

	// Verify the job now points at the new SHA
	updatedJob, err := db.GetJobByID(job.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if updatedJob.GitRef != newSHA {
		t.Errorf("expected git_ref=%s, got %s", newSHA, updatedJob.GitRef)
	}

	// Verify the review is reachable via the new SHA
	review, err := db.GetReviewByCommitSHA(newSHA)
	if err != nil {
		t.Fatalf("GetReviewByCommitSHA(%s): %v", newSHA, err)
	}
	if review == nil {
		t.Fatal("review should be reachable via new SHA after remap")
	}
	if !strings.Contains(review.Output, "LGTM") {
		t.Errorf("unexpected review output: %s", review.Output)
	}
}

// TestRemapAfterAmendMessageOnly exercises the message-only amend flow.
func TestRemapAfterAmendMessageOnly(t *testing.T) {
	server, db, _ := newTestServer(t)

	repo := newGitRepo(t)
	repo.commitFile("file.txt", "content", "original message")
	oldSHA := repo.headSHA()

	repoDir, err := filepath.EvalSymlinks(repo.dir)
	if err != nil {
		repoDir = repo.dir
	}

	dbRepo, err := db.GetOrCreateRepo(repoDir)
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	patchID := gitpkg.GetPatchID(repo.dir, oldSHA)
	if patchID == "" {
		t.Fatal("expected non-empty patch-id")
	}

	commit, err := db.GetOrCreateCommit(
		dbRepo.ID, oldSHA, "Test", "original message", time.Now(),
	)
	if err != nil {
		t.Fatalf("GetOrCreateCommit: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{
		RepoID:   dbRepo.ID,
		CommitID: commit.ID,
		GitRef:   oldSHA,
		Agent:    "test",
		PatchID:  patchID,
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	_, err = db.ClaimJob("worker-amend")
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	err = db.CompleteJob(job.ID, "test", "prompt", "looks good")
	if err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}

	// Amend message only
	repo.run("commit", "--amend", "-m", "amended message")
	newSHA := repo.headSHA()

	if oldSHA == newSHA {
		t.Fatal("SHAs should differ after amend")
	}

	newPatchID := gitpkg.GetPatchID(repo.dir, newSHA)
	if patchID != newPatchID {
		t.Fatalf("patch-ids should match for message-only amend: %s != %s",
			patchID, newPatchID)
	}

	info, err := gitpkg.GetCommitInfo(repo.dir, newSHA)
	if err != nil {
		t.Fatalf("GetCommitInfo: %v", err)
	}

	reqData := RemapRequest{
		RepoPath: repoDir,
		Mappings: []RemapMapping{
			{
				OldSHA:    oldSHA,
				NewSHA:    newSHA,
				PatchID:   patchID,
				Author:    info.Author,
				Subject:   info.Subject,
				Timestamp: info.Timestamp.Format(time.RFC3339),
			},
		},
	}
	req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/remap", reqData)
	w := httptest.NewRecorder()
	server.handleRemap(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]int
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["remapped"] != 1 {
		t.Errorf("expected remapped=1, got %d", result["remapped"])
	}

	// Review should be reachable via new SHA
	review, err := db.GetReviewByCommitSHA(newSHA)
	if err != nil {
		t.Fatalf("GetReviewByCommitSHA(%s): %v", newSHA, err)
	}
	if review == nil {
		t.Fatal("review should be reachable via new SHA after amend remap")
	}

	// Old SHA should no longer find the review
	oldReview, err := db.GetReviewByCommitSHA(oldSHA)
	if err == nil && oldReview != nil {
		t.Error("old SHA should not find the review after remap")
	}
}
