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
	t            *testing.T
	dir          string
	resolvedPath string
}

func (g *gitHelper) run(args ...string) {
	g.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = g.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		g.t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

func (g *gitHelper) path() string {
	if g.resolvedPath != "" {
		return g.resolvedPath
	}
	return g.dir
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

	// Resolve symlinks for macOS /var -> /private/var
	resolvedPath, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolvedPath = dir
	}

	g := &gitHelper{t: t, dir: dir, resolvedPath: resolvedPath}
	g.run("init", "-b", "main")
	g.run("config", "user.email", "test@test.com")
	g.run("config", "user.name", "Test")
	return g
}

type remapContext struct {
	server  *Server
	db      *storage.DB
	repo    *gitHelper
	oldSHA  string
	patchID string
	jobID   int64
}

func setupRemapScenario(t *testing.T, setupGit func(*gitHelper)) *remapContext {
	server, db, _ := newTestServer(t)
	repo := newGitRepo(t)

	if setupGit != nil {
		setupGit(repo)
	}

	dbRepo, err := db.GetOrCreateRepo(repo.path())
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	oldSHA := repo.headSHA()
	patchID := gitpkg.GetPatchID(repo.path(), oldSHA)
	if patchID == "" {
		t.Fatal("expected non-empty patch-id for feature commit")
	}

	// Create commit record
	commit, err := db.GetOrCreateCommit(
		dbRepo.ID, oldSHA, "Test", "setup message", time.Now(),
	)
	if err != nil {
		t.Fatalf("GetOrCreateCommit: %v", err)
	}

	// Enqueue job
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

	// Claim and complete job
	_, err = db.ClaimJob("worker-int")
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	err = db.CompleteJob(job.ID, "test", "prompt", "LGTM - no issues found")
	if err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}

	return &remapContext{
		server:  server,
		db:      db,
		repo:    repo,
		oldSHA:  oldSHA,
		patchID: patchID,
		jobID:   job.ID,
	}
}

func verifyRemap(t *testing.T, ctx *remapContext, newSHA string) {
	t.Helper()

	info, err := gitpkg.GetCommitInfo(ctx.repo.path(), newSHA)
	if err != nil {
		t.Fatalf("GetCommitInfo: %v", err)
	}

	// POST /api/remap
	reqData := RemapRequest{
		RepoPath: ctx.repo.path(),
		Mappings: []RemapMapping{
			{
				OldSHA:    ctx.oldSHA,
				NewSHA:    newSHA,
				PatchID:   ctx.patchID,
				Author:    info.Author,
				Subject:   info.Subject,
				Timestamp: info.Timestamp.Format(time.RFC3339),
			},
		},
	}
	req := testutil.MakeJSONRequest(t, http.MethodPost, "/api/remap", reqData)
	w := httptest.NewRecorder()
	ctx.server.handleRemap(w, req)

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
	updatedJob, err := ctx.db.GetJobByID(ctx.jobID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if updatedJob.GitRef != newSHA {
		t.Errorf("expected git_ref=%s, got %s", newSHA, updatedJob.GitRef)
	}

	// Verify the review is reachable via the new SHA
	review, err := ctx.db.GetReviewByCommitSHA(newSHA)
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

// TestRemapAfterRebase exercises the rebase remap flow by calling
// the HTTP handler directly with real git operations. This validates
// the full DB flow (patch-id matching, commit creation, git_ref
// update) without requiring a running daemon. End-to-end testing
// through the actual post-rewrite hook → CLI → daemon path is
// covered by manual testing (see design doc verification section).
func TestRemapAfterRebase(t *testing.T) {
	ctx := setupRemapScenario(t, func(repo *gitHelper) {
		repo.commitFile("base.txt", "base content", "initial commit")
		// Create feature branch with a commit
		repo.run("checkout", "-b", "feature")
		repo.commitFile("feature.txt", "feature content", "add feature")
	})

	// Advance main so rebase has work to do
	ctx.repo.run("checkout", "main")
	ctx.repo.commitFile("main2.txt", "more main", "advance main")

	// Rebase feature onto main
	ctx.repo.run("checkout", "feature")
	ctx.repo.run("rebase", "main")
	newSHA := ctx.repo.headSHA()

	if ctx.oldSHA == newSHA {
		t.Fatal("SHAs should differ after rebase")
	}

	newPatchID := gitpkg.GetPatchID(ctx.repo.path(), newSHA)
	if ctx.patchID != newPatchID {
		t.Fatalf("patch-ids should match after clean rebase: %s != %s",
			ctx.patchID, newPatchID)
	}

	verifyRemap(t, ctx, newSHA)
}

// TestRemapAfterAmendMessageOnly exercises the message-only amend flow.
func TestRemapAfterAmendMessageOnly(t *testing.T) {
	ctx := setupRemapScenario(t, func(repo *gitHelper) {
		repo.commitFile("file.txt", "content", "original message")
	})

	// Amend message only
	ctx.repo.run("commit", "--amend", "-m", "amended message")
	newSHA := ctx.repo.headSHA()

	if ctx.oldSHA == newSHA {
		t.Fatal("SHAs should differ after amend")
	}

	newPatchID := gitpkg.GetPatchID(ctx.repo.path(), newSHA)
	if ctx.patchID != newPatchID {
		t.Fatalf("patch-ids should match for message-only amend: %s != %s",
			ctx.patchID, newPatchID)
	}

	verifyRemap(t, ctx, newSHA)

	// Old SHA should no longer find the review
	oldReview, err := ctx.db.GetReviewByCommitSHA(ctx.oldSHA)
	if err == nil && oldReview != nil {
		t.Error("old SHA should not find the review after remap")
	}
}
