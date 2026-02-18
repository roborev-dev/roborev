package daemon

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

// ciPollerHarness bundles DB, repo, config, and poller for CI poller tests.
type ciPollerHarness struct {
	DB       *storage.DB
	RepoPath string
	Repo     *storage.Repo
	Cfg      *config.Config
	Poller   *CIPoller
}

// newCIPollerHarness creates a test DB, temp dir repo, and a CIPoller with
// git stubs that succeed without doing real git operations.
func newCIPollerHarness(t *testing.T, identity string) *ciPollerHarness {
	t.Helper()
	db := testutil.OpenTestDB(t)
	repoPath := t.TempDir()
	repo, err := db.GetOrCreateRepo(repoPath, identity)
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.CI.Enabled = true
	p := NewCIPoller(db, NewStaticConfig(cfg), nil)
	return &ciPollerHarness{DB: db, RepoPath: repoPath, Repo: repo, Cfg: cfg, Poller: p}
}

// stubProcessPRGit wires up git stubs on the poller so processPR doesn't
// call real git. mergeBaseFn returns "base-" + ref2.
// Also stubs agent resolution so tests don't need real agents in PATH.
func (h *ciPollerHarness) stubProcessPRGit() {
	h.Poller.gitFetchFn = func(context.Context, string) error { return nil }
	h.Poller.gitFetchPRHeadFn = func(context.Context, string, int) error { return nil }
	h.Poller.mergeBaseFn = func(_, _, ref2 string) (string, error) { return "base-" + ref2, nil }
	h.Poller.agentResolverFn = func(name string) (string, error) { return name, nil }
}

// seedBatchJob creates a CI batch, enqueues a job, and links them.
func (h *ciPollerHarness) seedBatchJob(t *testing.T, ghRepo string, prNum int, headSHA, gitRef, agent, reviewType string) (*storage.CIPRBatch, *storage.ReviewJob) {
	t.Helper()
	batch, _, err := h.DB.CreateCIBatch(ghRepo, prNum, headSHA, 1)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}
	job, err := h.DB.EnqueueJob(storage.EnqueueOpts{
		RepoID: h.Repo.ID, GitRef: gitRef, Agent: agent, ReviewType: reviewType,
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if err := h.DB.RecordBatchJob(batch.ID, job.ID); err != nil {
		t.Fatalf("RecordBatchJob: %v", err)
	}
	return batch, job
}

// markJobDoneWithReview sets a job to "done" and inserts a review row.
func (h *ciPollerHarness) markJobDoneWithReview(t *testing.T, jobID int64, agent, output string) {
	t.Helper()
	if _, err := h.DB.Exec(`UPDATE review_jobs SET status='done' WHERE id = ?`, jobID); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	if _, err := h.DB.Exec(`INSERT INTO reviews (job_id, agent, prompt, output) VALUES (?, ?, 'p', ?)`, jobID, agent, output); err != nil {
		t.Fatalf("insert review: %v", err)
	}
}

// markJobFailed sets a job to "failed" with the given error text.
func (h *ciPollerHarness) markJobFailed(t *testing.T, jobID int64, errText string) {
	t.Helper()
	if _, err := h.DB.Exec(`UPDATE review_jobs SET status='failed', error=? WHERE id = ?`, errText, jobID); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
}

// markJobCanceled sets a job to "canceled" with the given error text.
func (h *ciPollerHarness) markJobCanceled(t *testing.T, jobID int64, errText string) {
	t.Helper()
	if _, err := h.DB.Exec(`UPDATE review_jobs SET status='canceled', error=? WHERE id = ?`, errText, jobID); err != nil {
		t.Fatalf("mark canceled: %v", err)
	}
}

// assertContainsAll checks that s contains every substring, failing the test for each miss.
func assertContainsAll(t *testing.T, s string, wantLabel string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("%s missing %q", wantLabel, sub)
		}
	}
}

func TestBuildSynthesisPrompt(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Output: "No issues found.", Status: "done"},
		{JobID: 2, Agent: "gemini", ReviewType: "review", Output: "Consider error handling in foo.go:42", Status: "done"},
		{JobID: 3, Agent: "codex", ReviewType: "review", Status: "failed", Error: "timeout"},
	}

	prompt := buildSynthesisPrompt(reviews, "")

	assertContainsAll(t, prompt, "prompt",
		"Deduplicate findings",
		"Organize by severity",
		"### Review 1: Agent=codex, Type=security",
		"### Review 2: Agent=gemini, Type=review",
		"[FAILED]",
		"No issues found.",
		"foo.go:42",
		"(no output",
	)
}

func TestFormatRawBatchComment(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Output: "Finding A", Status: "done"},
		{JobID: 2, Agent: "gemini", ReviewType: "review", Status: "failed", Error: "timeout"},
	}

	comment := formatRawBatchComment(reviews, "abc123def456")

	assertContainsAll(t, comment, "comment",
		"## roborev: Combined Review (`abc123de`)",
		"Synthesis unavailable",
		"<details>",
		"Agent: codex | Type: security | Status: done",
		"Finding A",
		"Agent: gemini | Type: review | Status: failed",
		"**Error:** Review failed. Check daemon logs for details.",
	)
}

func TestFormatSynthesizedComment(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Status: "done"},
		{JobID: 2, Agent: "gemini", ReviewType: "review", Status: "done"},
	}

	output := "All clean. No critical findings."
	comment := formatSynthesizedComment(output, reviews, "abc123def456")

	assertContainsAll(t, comment, "comment",
		"## roborev: Combined Review (`abc123de`)",
		"All clean. No critical findings.",
		"Synthesized from 2 reviews",
		"codex",
		"gemini",
		"security",
		"review",
	)
}

func TestFormatAllFailedComment(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Status: "failed", Error: "timeout"},
		{JobID: 2, Agent: "gemini", ReviewType: "review", Status: "failed", Error: "api error"},
	}

	comment := formatAllFailedComment(reviews, "abc123def456")

	assertContainsAll(t, comment, "comment",
		"## roborev: Review Failed (`abc123de`)",
		"All review jobs in this batch failed",
		"**codex** (security): failed",
		"**gemini** (review): failed",
		"Check daemon logs for error details.",
	)
}

func TestGhEnvForRepo_FiltersExistingTokens(t *testing.T) {
	// Set up a CIPoller with a pre-cached token (avoids JWT/API calls)
	provider := &GitHubAppTokenProvider{
		tokens: map[int64]*cachedToken{
			111111: {token: "ghs_app_token_123", expires: time.Now().Add(1 * time.Hour)},
		},
	}
	cfg := config.DefaultConfig()
	cfg.CI.GitHubAppInstallationID = 111111
	p := &CIPoller{tokenProvider: provider, cfgGetter: NewStaticConfig(cfg)}

	// Plant GH_TOKEN and GITHUB_TOKEN in env
	t.Setenv("GH_TOKEN", "personal_token")
	t.Setenv("GITHUB_TOKEN", "another_personal_token")

	env := p.ghEnvForRepo("acme/api")

	// Should contain our app token
	found := false
	for _, e := range env {
		if e == "GH_TOKEN=ghs_app_token_123" {
			found = true
		}
		if strings.HasPrefix(e, "GITHUB_TOKEN=") {
			t.Error("GITHUB_TOKEN should have been filtered out")
		}
		if strings.HasPrefix(e, "GH_TOKEN=personal_token") {
			t.Error("original GH_TOKEN should have been filtered out")
		}
	}
	if !found {
		t.Error("expected GH_TOKEN=ghs_app_token_123 in env")
	}
}

func TestGhEnvForRepo_NilProvider(t *testing.T) {
	p := &CIPoller{tokenProvider: nil}
	if env := p.ghEnvForRepo("acme/api"); env != nil {
		t.Errorf("expected nil env when no token provider, got %v", env)
	}
}

func TestGhEnvForRepo_UnknownOwner(t *testing.T) {
	// Token provider exists but no installation ID for the owner
	provider := &GitHubAppTokenProvider{
		tokens: make(map[int64]*cachedToken),
	}
	cfg := config.DefaultConfig()
	cfg.CI.GitHubAppInstallations = map[string]int64{"known-org": 111111}
	// No singular fallback
	p := &CIPoller{tokenProvider: provider, cfgGetter: NewStaticConfig(cfg)}

	env := p.ghEnvForRepo("unknown-org/repo")
	if env != nil {
		t.Errorf("expected nil env for unknown owner, got %v", env)
	}
}

func TestGhEnvForRepo_MultiInstallationRouting(t *testing.T) {
	// Two installations cached, verify correct one is used per repo
	provider := &GitHubAppTokenProvider{
		tokens: map[int64]*cachedToken{
			111111: {token: "ghs_token_wesm", expires: time.Now().Add(1 * time.Hour)},
			222222: {token: "ghs_token_org", expires: time.Now().Add(1 * time.Hour)},
		},
	}
	cfg := config.DefaultConfig()
	cfg.CI.GitHubAppInstallations = map[string]int64{
		"wesm":        111111,
		"roborev-dev": 222222,
	}
	p := &CIPoller{tokenProvider: provider, cfgGetter: NewStaticConfig(cfg)}

	// Check wesm repo uses wesm installation token
	env1 := p.ghEnvForRepo("wesm/my-repo")
	found1 := false
	for _, e := range env1 {
		if e == "GH_TOKEN=ghs_token_wesm" {
			found1 = true
		}
	}
	if !found1 {
		t.Error("expected wesm's token for wesm/my-repo")
	}

	// Check roborev-dev repo uses org installation token
	env2 := p.ghEnvForRepo("roborev-dev/other-repo")
	found2 := false
	for _, e := range env2 {
		if e == "GH_TOKEN=ghs_token_org" {
			found2 = true
		}
	}
	if !found2 {
		t.Error("expected roborev-dev's token for roborev-dev/other-repo")
	}
}

func TestGhEnvForRepo_CaseInsensitiveOwner(t *testing.T) {
	provider := &GitHubAppTokenProvider{
		tokens: map[int64]*cachedToken{
			111111: {token: "ghs_token_wesm", expires: time.Now().Add(1 * time.Hour)},
		},
	}
	cfg := config.DefaultConfig()
	cfg.CI.GitHubAppInstallations = map[string]int64{"wesm": 111111}
	p := &CIPoller{tokenProvider: provider, cfgGetter: NewStaticConfig(cfg)}

	// Uppercase owner in repo should still match lowercase config key
	env := p.ghEnvForRepo("Wesm/my-repo")
	found := false
	for _, e := range env {
		if e == "GH_TOKEN=ghs_token_wesm" {
			found = true
		}
	}
	if !found {
		t.Error("expected token for case-variant owner 'Wesm' matching config key 'wesm'")
	}
}

func TestFormatRawBatchComment_Truncation(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Output: strings.Repeat("x", 20000), Status: "done"},
	}

	comment := formatRawBatchComment(reviews, "abc123def456")
	if !strings.Contains(comment, "...(truncated)") {
		t.Error("expected truncation for large output")
	}
}

func TestCIPollerProcessPR_EnqueuesMatrix(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	h.Cfg.CI.ReviewTypes = []string{"security", "review"}
	h.Cfg.CI.Agents = []string{"codex", "gemini"}
	h.Cfg.CI.Model = "gpt-test"
	h.Poller = NewCIPoller(h.DB, NewStaticConfig(h.Cfg), nil)
	h.Poller.gitFetchFn = func(context.Context, string) error { return nil }
	h.Poller.gitFetchPRHeadFn = func(context.Context, string, int) error { return nil }
	h.Poller.agentResolverFn = func(name string) (string, error) { return name, nil }
	h.Poller.mergeBaseFn = func(_, ref1, ref2 string) (string, error) {
		if ref1 != "origin/main" {
			t.Fatalf("merge-base ref1=%q, want origin/main", ref1)
		}
		if ref2 != "head-sha-123" {
			t.Fatalf("merge-base ref2=%q, want head-sha-123", ref2)
		}
		return "base-sha-999", nil
	}

	err := h.Poller.processPR(context.Background(), "acme/api", ghPR{
		Number:      42,
		HeadRefOid:  "head-sha-123",
		BaseRefName: "main",
	}, h.Cfg)
	if err != nil {
		t.Fatalf("processPR: %v", err)
	}

	hasBatch, err := h.DB.HasCIBatch("acme/api", 42, "head-sha-123")
	if err != nil {
		t.Fatalf("HasCIBatch: %v", err)
	}
	if !hasBatch {
		t.Fatal("expected CI batch to be created")
	}

	jobs, err := h.DB.ListJobs("", h.RepoPath, 0, 0, storage.WithGitRef("base-sha-999..head-sha-123"))
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 4 {
		t.Fatalf("expected 4 jobs, got %d", len(jobs))
	}

	got := make(map[string]bool)
	for _, j := range jobs {
		if j.Reasoning != "thorough" {
			t.Errorf("job %d reasoning=%q, want thorough", j.ID, j.Reasoning)
		}
		if j.Model != "gpt-test" {
			t.Errorf("job %d model=%q, want gpt-test", j.ID, j.Model)
		}
		got[j.Agent+"|"+j.ReviewType] = true
	}
	want := []string{
		"codex|security",
		"codex|default",
		"gemini|security",
		"gemini|default",
	}
	for _, key := range want {
		if !got[key] {
			t.Errorf("missing job combination %q", key)
		}
	}
}

func TestCIPollerPollRepo_UsesPRListAndProcessesEach(t *testing.T) {
	h := newCIPollerHarness(t, "https://github.com/acme/api.git")
	h.Cfg.CI.ReviewTypes = []string{"security"}
	h.Cfg.CI.Agents = []string{"codex"}
	h.Poller = NewCIPoller(h.DB, NewStaticConfig(h.Cfg), nil)
	h.Poller.listOpenPRsFn = func(context.Context, string) ([]ghPR, error) {
		return []ghPR{
			{Number: 7, HeadRefOid: "11111111aaaaaaaa", BaseRefName: "main"},
			{Number: 8, HeadRefOid: "22222222bbbbbbbb", BaseRefName: "main"},
		}, nil
	}
	h.stubProcessPRGit()

	if err := h.Poller.pollRepo(context.Background(), "acme/api", h.Cfg); err != nil {
		t.Fatalf("pollRepo: %v", err)
	}

	hasA, err := h.DB.HasCIBatch("acme/api", 7, "11111111aaaaaaaa")
	if err != nil {
		t.Fatalf("HasCIBatch A: %v", err)
	}
	hasB, err := h.DB.HasCIBatch("acme/api", 8, "22222222bbbbbbbb")
	if err != nil {
		t.Fatalf("HasCIBatch B: %v", err)
	}
	if !hasA || !hasB {
		t.Fatalf("expected both batches to exist, got hasA=%v hasB=%v", hasA, hasB)
	}
}

func TestCIPollerStartStopHealth(t *testing.T) {
	db := testutil.OpenTestDB(t)
	cfg := config.DefaultConfig()
	cfg.CI.Enabled = true
	cfg.CI.PollInterval = "10s" // <30s should clamp to default

	p := NewCIPoller(db, NewStaticConfig(cfg), NewBroadcaster())

	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	healthy, msg := p.HealthCheck()
	if !healthy || msg != "running" {
		t.Fatalf("HealthCheck after Start = (%v, %q), want (true, running)", healthy, msg)
	}

	p.Stop()

	healthy, msg = p.HealthCheck()
	if healthy || msg != "not running" {
		t.Fatalf("HealthCheck after Stop = (%v, %q), want (false, not running)", healthy, msg)
	}
}

func TestCIPollerHandleBatchJobDone_PartialBatchDoesNotPost(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")

	// Batch expects 2 jobs but we only seed 1 — partial
	batch, _, err := h.DB.CreateCIBatch("acme/api", 1, "sha", 2)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}
	job, err := h.DB.EnqueueJob(storage.EnqueueOpts{RepoID: h.Repo.ID, GitRef: "a..b", Agent: "codex", ReviewType: "security"})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if err := h.DB.RecordBatchJob(batch.ID, job.ID); err != nil {
		t.Fatalf("RecordBatchJob: %v", err)
	}

	posted := 0
	h.Poller.postPRCommentFn = func(string, int, string) error {
		posted++
		return nil
	}

	h.Poller.handleBatchJobDone(batch, job.ID, true)

	if posted != 0 {
		t.Fatalf("expected no PR comment yet, got %d", posted)
	}
}

func TestCIPollerHandleBatchJobDone_CompleteBatchPostsAndFinalizes(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	batch, job := h.seedBatchJob(t, "acme/api", 2, "sha", "a..b", "codex", "security")
	h.markJobDoneWithReview(t, job.ID, "codex", "No issues found.")

	var postedRepo string
	var postedPR int
	var postedBody string
	h.Poller.postPRCommentFn = func(repo string, pr int, body string) error {
		postedRepo = repo
		postedPR = pr
		postedBody = body
		return nil
	}

	h.Poller.handleBatchJobDone(batch, job.ID, true)

	if postedRepo != "acme/api" || postedPR != 2 {
		t.Fatalf("posted to %s#%d, want acme/api#2", postedRepo, postedPR)
	}
	if !strings.Contains(postedBody, "roborev") {
		t.Fatalf("expected roborev comment body, got: %q", postedBody)
	}

	var synthesized int
	var claimedAt sql.NullString
	if err := h.DB.QueryRow(`SELECT synthesized, claimed_at FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&synthesized, &claimedAt); err != nil {
		t.Fatalf("query batch: %v", err)
	}
	if synthesized != 1 {
		t.Fatalf("expected synthesized=1, got %d", synthesized)
	}
	if claimedAt.Valid {
		t.Fatalf("expected claimed_at to be cleared after finalize, got %q", claimedAt.String)
	}
}

func TestCIPollerReconcileStaleBatches_PostsCanceledJobsAsFailed(t *testing.T) {
	h := newCIPollerHarness(t, "https://github.com/acme/api")
	batch, job := h.seedBatchJob(t, "acme/api", 9, "sha", "a..b", "codex", "security")
	h.markJobCanceled(t, job.ID, "manual cancel")

	var postedBody string
	h.Poller.postPRCommentFn = func(_ string, _ int, body string) error {
		postedBody = body
		return nil
	}

	h.Poller.reconcileStaleBatches()

	if !strings.Contains(postedBody, "All review jobs in this batch failed.") {
		t.Fatalf("expected all-failed comment, got: %q", postedBody)
	}

	var completed, failed int
	if err := h.DB.QueryRow(`SELECT completed_jobs, failed_jobs FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&completed, &failed); err != nil {
		t.Fatalf("query reconciled counts: %v", err)
	}
	if completed != 0 || failed != 1 {
		t.Fatalf("expected reconciled counts 0/1, got %d/%d", completed, failed)
	}
}

func TestCIPollerHandleReviewCompleted_LegacyCIReviewPostsComment(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")

	job, err := h.DB.EnqueueJob(storage.EnqueueOpts{
		RepoID: h.Repo.ID, GitRef: "a..b", Agent: "codex", ReviewType: "security",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	h.markJobDoneWithReview(t, job.ID, "codex", "No issues found.")
	if err := h.DB.RecordCIReview("acme/api", 12, "head-sha", job.ID); err != nil {
		t.Fatalf("RecordCIReview: %v", err)
	}

	called := 0
	h.Poller.postPRCommentFn = func(repo string, pr int, body string) error {
		called++
		if repo != "acme/api" || pr != 12 {
			t.Fatalf("posted to %s#%d, want acme/api#12", repo, pr)
		}
		if !strings.Contains(body, "roborev") {
			t.Fatalf("unexpected body: %q", body)
		}
		return nil
	}

	h.Poller.handleReviewCompleted(Event{JobID: job.ID, Verdict: "P"})

	if called != 1 {
		t.Fatalf("expected one post call, got %d", called)
	}
}

func TestCIPollerHandleReviewFailed_BatchPath(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	batch, job := h.seedBatchJob(t, "acme/api", 13, "head-sha", "a..b", "codex", "security")
	_ = batch
	h.markJobFailed(t, job.ID, "timeout")

	var postedBody string
	h.Poller.postPRCommentFn = func(_ string, _ int, body string) error {
		postedBody = body
		return nil
	}

	h.Poller.handleReviewFailed(Event{JobID: job.ID})

	if !strings.Contains(postedBody, "Review Failed") {
		t.Fatalf("expected failure comment, got: %q", postedBody)
	}
}

func TestCIPollerPostBatchResults_SynthesisPathUsesMock(t *testing.T) {
	h := newCIPollerHarness(t, "https://github.com/acme/api.git")

	batch, _, err := h.DB.CreateCIBatch("acme/api", 14, "head-sha", 2)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}
	job1, err := h.DB.EnqueueJob(storage.EnqueueOpts{
		RepoID: h.Repo.ID, GitRef: "a..b", Agent: "codex", ReviewType: "security",
	})
	if err != nil {
		t.Fatalf("EnqueueJob job1: %v", err)
	}
	job2, err := h.DB.EnqueueJob(storage.EnqueueOpts{
		RepoID: h.Repo.ID, GitRef: "a..b", Agent: "gemini", ReviewType: "review",
	})
	if err != nil {
		t.Fatalf("EnqueueJob job2: %v", err)
	}
	if err := h.DB.RecordBatchJob(batch.ID, job1.ID); err != nil {
		t.Fatalf("RecordBatchJob job1: %v", err)
	}
	if err := h.DB.RecordBatchJob(batch.ID, job2.ID); err != nil {
		t.Fatalf("RecordBatchJob job2: %v", err)
	}
	h.markJobDoneWithReview(t, job1.ID, "codex", "finding A")
	h.markJobFailed(t, job2.ID, "timeout")

	h.Poller.synthesizeFn = func(_ *storage.CIPRBatch, _ []storage.BatchReviewResult, _ *config.Config) (string, error) {
		return "SYNTHESIZED-RESULT", nil
	}

	var postedBody string
	h.Poller.postPRCommentFn = func(_ string, _ int, body string) error {
		postedBody = body
		return nil
	}

	h.Poller.postBatchResults(batch)

	if !strings.Contains(postedBody, "SYNTHESIZED-RESULT") {
		t.Fatalf("expected synthesized output, got: %q", postedBody)
	}
}

func TestCIPollerPostBatchResults_PostFailureUnclaimsBatch(t *testing.T) {
	h := newCIPollerHarness(t, "https://github.com/acme/api.git")
	batch, job := h.seedBatchJob(t, "acme/api", 15, "head-sha", "a..b", "codex", "security")
	h.markJobFailed(t, job.ID, "timeout")

	h.Poller.postPRCommentFn = func(string, int, string) error {
		return context.DeadlineExceeded
	}

	h.Poller.postBatchResults(batch)

	var synthesized int
	if err := h.DB.QueryRow(`SELECT synthesized FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&synthesized); err != nil {
		t.Fatalf("query batch: %v", err)
	}
	if synthesized != 0 {
		t.Fatalf("expected batch to be unclaimed (synthesized=0), got %d", synthesized)
	}
}

func TestCIPollerFindLocalRepo_PartialIdentityFallback(t *testing.T) {
	h := newCIPollerHarness(t, "ssh://git@github.com/acme/api.git")

	found, err := h.Poller.findLocalRepo("acme/api")
	if err != nil {
		t.Fatalf("findLocalRepo: %v", err)
	}
	if found.ID != h.Repo.ID {
		t.Fatalf("found repo id %d, want %d", found.ID, h.Repo.ID)
	}
}

func TestCIPollerFindLocalRepo_SkipsPlaceholders(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// Create a sync placeholder (root_path == identity)
	identity := "git@github.com:acme/api.git"
	_, err := db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES (?, ?, ?)`,
		identity, "api", identity)
	if err != nil {
		t.Fatalf("insert placeholder: %v", err)
	}

	cfg := config.DefaultConfig()
	p := NewCIPoller(db, NewStaticConfig(cfg), nil)

	// With only a placeholder, should get "no local repo found"
	_, err = p.findLocalRepo("acme/api")
	if err == nil {
		t.Fatal("expected error when only placeholder exists")
	}
	if !strings.Contains(err.Error(), "no local repo found") {
		t.Fatalf("expected 'no local repo found' error, got: %v", err)
	}

	// Add a real local checkout — should find it and skip the placeholder
	repoPath := t.TempDir()
	repo, err := db.GetOrCreateRepo(repoPath, identity)
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	found, err := p.findLocalRepo("acme/api")
	if err != nil {
		t.Fatalf("findLocalRepo with real repo: %v", err)
	}
	if found.ID != repo.ID {
		t.Errorf("found repo id %d, want %d", found.ID, repo.ID)
	}
	if found.RootPath != repoPath {
		t.Errorf("found repo root_path %q, want %q", found.RootPath, repoPath)
	}
}

func TestCIPollerProcessPR_WhitespaceReasoning(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	h.Cfg.CI.ReviewTypes = []string{"security"}
	h.Cfg.CI.Agents = []string{"codex"}
	h.Poller = NewCIPoller(h.DB, NewStaticConfig(h.Cfg), nil)
	h.stubProcessPRGit()
	h.Poller.mergeBaseFn = func(_, _, _ string) (string, error) { return "base-sha", nil }

	if err := os.WriteFile(h.RepoPath+"/.roborev.toml", []byte("[ci]\nreasoning = \"   \"\n"), 0644); err != nil {
		t.Fatalf("write .roborev.toml: %v", err)
	}

	err := h.Poller.processPR(context.Background(), "acme/api", ghPR{
		Number: 50, HeadRefOid: "whitespace-reasoning-sha", BaseRefName: "main",
	}, h.Cfg)
	if err != nil {
		t.Fatalf("processPR: %v", err)
	}

	jobs, err := h.DB.ListJobs("", h.RepoPath, 0, 0, storage.WithGitRef("base-sha..whitespace-reasoning-sha"))
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Reasoning != "thorough" {
		t.Errorf("reasoning=%q, want thorough (whitespace should fall back to default)", jobs[0].Reasoning)
	}
}

func TestCIPollerProcessPR_InvalidReasoning(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	h.Cfg.CI.ReviewTypes = []string{"security"}
	h.Cfg.CI.Agents = []string{"codex"}
	h.Poller = NewCIPoller(h.DB, NewStaticConfig(h.Cfg), nil)
	h.stubProcessPRGit()
	h.Poller.mergeBaseFn = func(_, _, _ string) (string, error) { return "base-sha", nil }

	if err := os.WriteFile(h.RepoPath+"/.roborev.toml", []byte("[ci]\nreasoning = \"invalid\"\n"), 0644); err != nil {
		t.Fatalf("write .roborev.toml: %v", err)
	}

	err := h.Poller.processPR(context.Background(), "acme/api", ghPR{
		Number: 51, HeadRefOid: "invalid-reasoning-sha", BaseRefName: "main",
	}, h.Cfg)
	if err != nil {
		t.Fatalf("processPR: %v", err)
	}

	jobs, err := h.DB.ListJobs("", h.RepoPath, 0, 0, storage.WithGitRef("base-sha..invalid-reasoning-sha"))
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Reasoning != "thorough" {
		t.Errorf("reasoning=%q, want thorough (invalid should fall back to default)", jobs[0].Reasoning)
	}
}

func TestCIPollerSynthesizeBatchResults_WithTestAgent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CI.SynthesisAgent = "test"

	p := &CIPoller{}
	out, err := p.synthesizeBatchResults(
		&storage.CIPRBatch{ID: 1, HeadSHA: "deadbeef12345678"},
		[]storage.BatchReviewResult{
			{JobID: 1, Agent: "codex", ReviewType: "security", Output: "No issues found.", Status: "done"},
			{JobID: 2, Agent: "gemini", ReviewType: "review", Output: "Potential bug in x.go:10", Status: "done"},
		},
		cfg,
	)
	if err != nil {
		t.Fatalf("synthesizeBatchResults: %v", err)
	}
	if !strings.Contains(out, "## roborev: Combined Review (`deadbeef`)") {
		t.Fatalf("expected combined review header with SHA, got: %q", out)
	}
}

func TestCIPollerSynthesizeBatchResults_UsesRepoPath(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	h.Cfg.CI.SynthesisAgent = "test"
	batch, job := h.seedBatchJob(t, "acme/api", 20, "sha", "a..b", "codex", "security")
	h.markJobDoneWithReview(t, job.ID, "codex", "No issues found.")

	reviews, err := h.DB.GetBatchReviews(batch.ID)
	if err != nil {
		t.Fatalf("GetBatchReviews: %v", err)
	}

	out, err := h.Poller.synthesizeBatchResults(batch, reviews, h.Cfg)
	if err != nil {
		t.Fatalf("synthesizeBatchResults: %v", err)
	}
	// The test agent includes "Repo: <path>" in its output.
	// Verify the repo's root_path was passed (not empty string).
	if !strings.Contains(out, "Repo: "+h.RepoPath) {
		t.Errorf("expected synthesis to run with repoPath=%q, got output: %q", h.RepoPath, out)
	}
}

func TestCIPollerProcessPR_InvalidReviewType(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	h.Cfg.CI.ReviewTypes = []string{"security", "typo-type"}
	h.Poller = NewCIPoller(h.DB, NewStaticConfig(h.Cfg), nil)
	h.stubProcessPRGit()
	h.Poller.mergeBaseFn = func(_, _, _ string) (string, error) { return "base-sha", nil }

	err := h.Poller.processPR(context.Background(), "acme/api", ghPR{
		Number: 1, HeadRefOid: "head-sha", BaseRefName: "main",
	}, h.Cfg)
	if err == nil {
		t.Fatal("expected error for invalid review type")
	}
	if !strings.Contains(err.Error(), "invalid review_type") {
		t.Fatalf("expected 'invalid review_type' error, got: %v", err)
	}

	hasBatch, err := h.DB.HasCIBatch("acme/api", 1, "head-sha")
	if err != nil {
		t.Fatalf("HasCIBatch: %v", err)
	}
	if hasBatch {
		t.Fatal("expected no batch for invalid review type")
	}
}

func TestCIPollerProcessPR_EmptyReviewType(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	h.Cfg.CI.ReviewTypes = []string{""}
	h.Poller = NewCIPoller(h.DB, NewStaticConfig(h.Cfg), nil)
	h.stubProcessPRGit()
	h.Poller.mergeBaseFn = func(_, _, _ string) (string, error) { return "base-sha", nil }

	err := h.Poller.processPR(context.Background(), "acme/api", ghPR{
		Number: 2, HeadRefOid: "head-sha-2", BaseRefName: "main",
	}, h.Cfg)
	if err == nil {
		t.Fatal("expected error for empty review type")
	}
	if !strings.Contains(err.Error(), "invalid review_type") {
		t.Fatalf("expected 'invalid review_type' error, got: %v", err)
	}
}

func TestCIPollerProcessPR_DesignReviewType(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	h.Cfg.CI.ReviewTypes = []string{"design"}
	h.Cfg.CI.Agents = []string{"codex"}
	h.Poller = NewCIPoller(h.DB, NewStaticConfig(h.Cfg), nil)
	h.stubProcessPRGit()
	h.Poller.mergeBaseFn = func(_, _, _ string) (string, error) { return "base-sha", nil }

	err := h.Poller.processPR(context.Background(), "acme/api", ghPR{
		Number: 10, HeadRefOid: "design-head", BaseRefName: "main",
	}, h.Cfg)
	if err != nil {
		t.Fatalf("processPR: %v", err)
	}

	jobs, err := h.DB.ListJobs("", h.RepoPath, 0, 0, storage.WithGitRef("base-sha..design-head"))
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}

	if jobs[0].ReviewType != "design" {
		t.Errorf("ReviewType=%q, want design", jobs[0].ReviewType)
	}
}

func TestCIPollerProcessPR_AliasDeduplication(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	// All three are aliases for "default" — should be deduped to one
	h.Cfg.CI.ReviewTypes = []string{"default", "review", "general"}
	h.Cfg.CI.Agents = []string{"codex"}
	h.Poller = NewCIPoller(h.DB, NewStaticConfig(h.Cfg), nil)
	h.stubProcessPRGit()
	h.Poller.mergeBaseFn = func(_, _, _ string) (string, error) { return "base-sha", nil }

	err := h.Poller.processPR(context.Background(), "acme/api", ghPR{
		Number: 11, HeadRefOid: "dedup-head", BaseRefName: "main",
	}, h.Cfg)
	if err != nil {
		t.Fatalf("processPR: %v", err)
	}

	jobs, err := h.DB.ListJobs("", h.RepoPath, 0, 0, storage.WithGitRef("base-sha..dedup-head"))
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job (deduped from 3 aliases), got %d", len(jobs))
	}
	if jobs[0].ReviewType != "default" {
		t.Errorf("ReviewType=%q, want default", jobs[0].ReviewType)
	}
}

func TestCIPollerProcessPR_IncompleteBatchRecovery(t *testing.T) {
	t.Run("stale empty batch is recovered", func(t *testing.T) {
		h := newCIPollerHarness(t, "git@github.com:acme/api.git")
		h.Cfg.CI.ReviewTypes = []string{"security"}
		h.Cfg.CI.Agents = []string{"codex"}
		h.Poller = NewCIPoller(h.DB, NewStaticConfig(h.Cfg), nil)
		h.stubProcessPRGit()
		h.Poller.mergeBaseFn = func(_, _, _ string) (string, error) { return "base-sha", nil }

		batch, created, err := h.DB.CreateCIBatch("acme/api", 50, "head-sha-50", 1)
		if err != nil {
			t.Fatalf("CreateCIBatch: %v", err)
		}
		if !created {
			t.Fatal("expected to create batch")
		}
		if _, err := h.DB.Exec(`UPDATE ci_pr_batches SET created_at = datetime('now', '-5 minutes'), updated_at = datetime('now', '-2 minutes') WHERE id = ?`, batch.ID); err != nil {
			t.Fatalf("backdate batch: %v", err)
		}

		err = h.Poller.processPR(context.Background(), "acme/api", ghPR{
			Number: 50, HeadRefOid: "head-sha-50", BaseRefName: "main",
		}, h.Cfg)
		if err != nil {
			t.Fatalf("processPR: %v", err)
		}

		jobs, err := h.DB.ListJobs("", h.RepoPath, 0, 0, storage.WithGitRef("base-sha..head-sha-50"))
		if err != nil {
			t.Fatalf("ListJobs: %v", err)
		}
		if len(jobs) != 1 {
			t.Fatalf("expected 1 job after recovery, got %d", len(jobs))
		}
	})

	t.Run("staleness uses updated_at heartbeat", func(t *testing.T) {
		h := newCIPollerHarness(t, "git@github.com:acme/api.git")

		batch, _, err := h.DB.CreateCIBatch("acme/api", 51, "head-sha-51", 2)
		if err != nil {
			t.Fatalf("CreateCIBatch: %v", err)
		}

		stale, err := h.DB.IsBatchStale(batch.ID)
		if err != nil {
			t.Fatalf("IsBatchStale: %v", err)
		}
		if stale {
			t.Error("fresh batch should not be stale")
		}

		if _, err := h.DB.Exec(`UPDATE ci_pr_batches SET created_at = datetime('now', '-5 minutes'), updated_at = datetime('now', '-2 minutes') WHERE id = ?`, batch.ID); err != nil {
			t.Fatalf("backdate batch: %v", err)
		}
		stale, err = h.DB.IsBatchStale(batch.ID)
		if err != nil {
			t.Fatalf("IsBatchStale: %v", err)
		}
		if !stale {
			t.Error("batch with old updated_at should be stale")
		}

		job, err := h.DB.EnqueueJob(storage.EnqueueOpts{
			RepoID: h.Repo.ID, GitRef: "a..b", Agent: "codex", ReviewType: "security",
		})
		if err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}
		if err := h.DB.RecordBatchJob(batch.ID, job.ID); err != nil {
			t.Fatalf("RecordBatchJob: %v", err)
		}
		stale, err = h.DB.IsBatchStale(batch.ID)
		if err != nil {
			t.Fatalf("IsBatchStale after RecordBatchJob: %v", err)
		}
		if stale {
			t.Error("batch should not be stale after RecordBatchJob heartbeat")
		}

		ids, err := h.DB.GetBatchJobIDs(batch.ID)
		if err != nil {
			t.Fatalf("GetBatchJobIDs: %v", err)
		}
		if len(ids) != 1 || ids[0] != job.ID {
			t.Fatalf("GetBatchJobIDs = %v, want [%d]", ids, job.ID)
		}
	})

	t.Run("fresh incomplete batch is skipped", func(t *testing.T) {
		h := newCIPollerHarness(t, "git@github.com:acme/api.git")
		h.Cfg.CI.ReviewTypes = []string{"security"}
		h.Cfg.CI.Agents = []string{"codex"}
		h.Poller = NewCIPoller(h.DB, NewStaticConfig(h.Cfg), nil)
		h.stubProcessPRGit()
		h.Poller.mergeBaseFn = func(_, _, _ string) (string, error) { return "base-sha", nil }

		_, created, err := h.DB.CreateCIBatch("acme/api", 52, "head-sha-52", 1)
		if err != nil {
			t.Fatalf("CreateCIBatch: %v", err)
		}
		if !created {
			t.Fatal("expected to create batch")
		}

		err = h.Poller.processPR(context.Background(), "acme/api", ghPR{
			Number: 52, HeadRefOid: "head-sha-52", BaseRefName: "main",
		}, h.Cfg)
		if err != nil {
			t.Fatalf("processPR: %v", err)
		}

		jobs, err := h.DB.ListJobs("", h.RepoPath, 0, 0, storage.WithGitRef("base-sha..head-sha-52"))
		if err != nil {
			t.Fatalf("ListJobs: %v", err)
		}
		if len(jobs) != 0 {
			t.Fatalf("expected 0 jobs (fresh batch skipped), got %d", len(jobs))
		}
	})
}

func TestCIPollerFindLocalRepo_AmbiguousRepoError(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// Create two repos with the same identity (different local paths)
	if _, err := db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES (?, ?, ?)`,
		"/tmp/clone1", "api", "https://github.com/acme/api.git"); err != nil {
		t.Fatalf("insert repo1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES (?, ?, ?)`,
		"/tmp/clone2", "api", "https://github.com/acme/api.git"); err != nil {
		t.Fatalf("insert repo2: %v", err)
	}

	cfg := config.DefaultConfig()
	p := NewCIPoller(db, NewStaticConfig(cfg), nil)

	_, err := p.findLocalRepo("acme/api")
	if err == nil {
		t.Fatal("expected error for ambiguous repo match")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity error, got: %v", err)
	}
}

func TestCIPollerFindLocalRepo_PartialIdentityAmbiguity(t *testing.T) {
	db := testutil.OpenTestDB(t)

	// Create two repos with identities that DON'T match the exact patterns
	// tried by findLocalRepo (which only checks github.com), so they fall
	// through to partial suffix matching where both match "acme/widgets"
	if _, err := db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES (?, ?, ?)`,
		"/tmp/clone-ghe1", "widgets", "git@ghe.corp.com:acme/widgets.git"); err != nil {
		t.Fatalf("insert repo1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO repos (root_path, name, identity) VALUES (?, ?, ?)`,
		"/tmp/clone-ghe2", "widgets", "https://ghe.corp.com/acme/widgets"); err != nil {
		t.Fatalf("insert repo2: %v", err)
	}

	cfg := config.DefaultConfig()
	p := NewCIPoller(db, NewStaticConfig(cfg), nil)

	_, err := p.findLocalRepo("acme/widgets")
	if err == nil {
		t.Fatal("expected error for ambiguous partial repo match")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity error, got: %v", err)
	}
}

func TestBuildSynthesisPrompt_TruncatesLargeOutputs(t *testing.T) {
	largeOutput := strings.Repeat("x", 20000)
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Output: largeOutput, Status: "done"},
	}

	prompt := buildSynthesisPrompt(reviews, "")

	if len(prompt) > 16500 { // 15k truncated + headers/instructions
		t.Errorf("synthesis prompt too large (%d chars), expected truncation", len(prompt))
	}
	if !strings.Contains(prompt, "...(truncated)") {
		t.Error("expected truncation marker in synthesis prompt")
	}
}

func TestCIPollerProcessPR_RepoOverrides(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	h.Cfg.CI.ReviewTypes = []string{"security", "review"}
	h.Cfg.CI.Agents = []string{"codex", "gemini"}
	h.Poller = NewCIPoller(h.DB, NewStaticConfig(h.Cfg), nil)
	h.stubProcessPRGit()
	h.Poller.mergeBaseFn = func(_, _, _ string) (string, error) { return "base-sha", nil }

	if err := os.WriteFile(h.RepoPath+"/.roborev.toml", []byte("[ci]\nagents = [\"codex\"]\nreview_types = [\"review\"]\nreasoning = \"fast\"\n"), 0644); err != nil {
		t.Fatalf("write .roborev.toml: %v", err)
	}

	err := h.Poller.processPR(context.Background(), "acme/api", ghPR{
		Number: 99, HeadRefOid: "repo-override-sha", BaseRefName: "main",
	}, h.Cfg)
	if err != nil {
		t.Fatalf("processPR: %v", err)
	}

	jobs, err := h.DB.ListJobs("", h.RepoPath, 0, 0, storage.WithGitRef("base-sha..repo-override-sha"))
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job (repo override), got %d", len(jobs))
	}

	j := jobs[0]
	if j.ReviewType != "default" {
		t.Errorf("review_type=%q, want default (canonicalized from review)", j.ReviewType)
	}
	if j.Agent != "codex" {
		t.Errorf("agent=%q, want codex", j.Agent)
	}
	if j.Reasoning != "fast" {
		t.Errorf("reasoning=%q, want fast", j.Reasoning)
	}
}

func TestBuildSynthesisPrompt_SanitizesErrors(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Status: "failed", Error: "secret-token-abc123: auth error"},
	}
	prompt := buildSynthesisPrompt(reviews, "")
	if strings.Contains(prompt, "secret-token-abc123") {
		t.Error("raw error text should not appear in synthesis prompt")
	}
	if !strings.Contains(prompt, "[FAILED]") {
		t.Error("expected [FAILED] marker in synthesis prompt")
	}
}

func TestBuildSynthesisPrompt_WithMinSeverity(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Output: "No issues found.", Status: "done"},
	}

	t.Run("no filter when empty", func(t *testing.T) {
		prompt := buildSynthesisPrompt(reviews, "")
		if strings.Contains(prompt, "Omit findings below") {
			t.Error("expected no severity filter instruction when minSeverity is empty")
		}
	})

	t.Run("no filter when low", func(t *testing.T) {
		prompt := buildSynthesisPrompt(reviews, "low")
		if strings.Contains(prompt, "Omit findings below") {
			t.Error("expected no severity filter instruction when minSeverity is low")
		}
	})

	t.Run("filter for medium", func(t *testing.T) {
		prompt := buildSynthesisPrompt(reviews, "medium")
		assertContainsAll(t, prompt, "prompt",
			"Omit findings below medium severity",
			"Only include Medium, High, and Critical findings.",
		)
	})

	t.Run("filter for high", func(t *testing.T) {
		prompt := buildSynthesisPrompt(reviews, "high")
		assertContainsAll(t, prompt, "prompt",
			"Omit findings below high severity",
			"Only include High and Critical findings.",
		)
	})

	t.Run("filter for critical", func(t *testing.T) {
		prompt := buildSynthesisPrompt(reviews, "critical")
		assertContainsAll(t, prompt, "prompt",
			"Omit findings below critical severity",
			"Only include Critical findings.",
		)
	})
}

func TestResolveMinSeverity(t *testing.T) {
	t.Run("empty global, no repo config", func(t *testing.T) {
		got := resolveMinSeverity("", t.TempDir(), "acme/api")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("global value used when no repo config", func(t *testing.T) {
		got := resolveMinSeverity("high", t.TempDir(), "acme/api")
		if got != "high" {
			t.Errorf("got %q, want high", got)
		}
	})

	t.Run("repo override takes precedence over global", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(dir+"/.roborev.toml", []byte("[ci]\nmin_severity = \"critical\"\n"), 0644)
		got := resolveMinSeverity("low", dir, "acme/api")
		if got != "critical" {
			t.Errorf("got %q, want critical (repo override)", got)
		}
	})

	t.Run("invalid repo value falls back to global", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(dir+"/.roborev.toml", []byte("[ci]\nmin_severity = \"bogus\"\n"), 0644)
		got := resolveMinSeverity("medium", dir, "acme/api")
		if got != "medium" {
			t.Errorf("got %q, want medium (fallback to global)", got)
		}
	})

	t.Run("invalid global value returns empty", func(t *testing.T) {
		got := resolveMinSeverity("bogus", t.TempDir(), "acme/api")
		if got != "" {
			t.Errorf("got %q, want empty (invalid global ignored)", got)
		}
	})

	t.Run("empty repo override uses global", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(dir+"/.roborev.toml", []byte("[ci]\nreasoning = \"fast\"\n"), 0644)
		got := resolveMinSeverity("high", dir, "acme/api")
		if got != "high" {
			t.Errorf("got %q, want high (repo has no min_severity)", got)
		}
	})

	t.Run("empty repoPath skips repo config", func(t *testing.T) {
		got := resolveMinSeverity("medium", "", "acme/api")
		if got != "medium" {
			t.Errorf("got %q, want medium", got)
		}
	})

	t.Run("global value is case-normalized", func(t *testing.T) {
		got := resolveMinSeverity("HIGH", t.TempDir(), "acme/api")
		if got != "high" {
			t.Errorf("got %q, want high (normalized)", got)
		}
	})
}

// initGitRepoWithOrigin creates a git repo with an initial commit and
// origin pointing to itself, so origin/main and GetDefaultBranch work.
func initGitRepoWithOrigin(t *testing.T) (dir string, runGit func(args ...string) string) {
	t.Helper()
	dir = t.TempDir()
	runGit = func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("init"), 0644)
	runGit("add", "-A")
	runGit("commit", "-m", "initial")
	runGit("remote", "add", "origin", dir)
	runGit("fetch", "origin")
	runGit("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return dir, runGit
}

func TestLoadCIRepoConfig_LoadsFromDefaultBranch(t *testing.T) {
	dir, runGit := initGitRepoWithOrigin(t)

	// Commit .roborev.toml on main with CI agents override
	os.WriteFile(filepath.Join(dir, ".roborev.toml"),
		[]byte("[ci]\nagents = [\"claude\"]\n"), 0644)
	runGit("add", ".roborev.toml")
	runGit("commit", "-m", "add config")
	runGit("fetch", "origin")

	cfg, err := loadCIRepoConfig(dir)
	if err != nil {
		t.Fatalf("loadCIRepoConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.CI.Agents) != 1 || cfg.CI.Agents[0] != "claude" {
		t.Errorf("agents=%v, want [claude]", cfg.CI.Agents)
	}
}

func TestLoadCIRepoConfig_FallsBackWhenNoConfigOnDefaultBranch(t *testing.T) {
	dir, _ := initGitRepoWithOrigin(t)

	// No .roborev.toml on origin/main, but put one in the working tree
	os.WriteFile(filepath.Join(dir, ".roborev.toml"),
		[]byte("[ci]\nagents = [\"codex\"]\n"), 0644)

	cfg, err := loadCIRepoConfig(dir)
	if err != nil {
		t.Fatalf("loadCIRepoConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected filesystem fallback config")
	}
	if len(cfg.CI.Agents) != 1 || cfg.CI.Agents[0] != "codex" {
		t.Errorf("agents=%v, want [codex] from filesystem fallback", cfg.CI.Agents)
	}
}

func TestLoadCIRepoConfig_PropagatesParseError(t *testing.T) {
	dir, runGit := initGitRepoWithOrigin(t)

	// Commit invalid TOML on main
	os.WriteFile(filepath.Join(dir, ".roborev.toml"),
		[]byte("this is not valid toml [[["), 0644)
	runGit("add", ".roborev.toml")
	runGit("commit", "-m", "add bad config")
	runGit("fetch", "origin")

	// Also put valid config in working tree — should NOT be used
	os.WriteFile(filepath.Join(dir, ".roborev.toml"),
		[]byte("[ci]\nagents = [\"codex\"]\n"), 0644)

	cfg, err := loadCIRepoConfig(dir)
	if err == nil {
		t.Fatalf("expected parse error, got cfg=%+v", cfg)
	}
	if !config.IsConfigParseError(err) {
		t.Errorf("expected ConfigParseError, got: %v", err)
	}
}

func TestCIPollerProcessPR_SetsPendingCommitStatus(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	h.Cfg.CI.ReviewTypes = []string{"security"}
	h.Cfg.CI.Agents = []string{"codex"}
	h.Poller = NewCIPoller(h.DB, NewStaticConfig(h.Cfg), nil)
	h.stubProcessPRGit()
	h.Poller.mergeBaseFn = func(_, _, _ string) (string, error) { return "base-sha", nil }

	type statusCall struct {
		repo, sha, state, desc string
	}
	var statusCalls []statusCall
	h.Poller.setCommitStatusFn = func(repo, sha, state, desc string) error {
		statusCalls = append(statusCalls, statusCall{repo, sha, state, desc})
		return nil
	}

	err := h.Poller.processPR(context.Background(), "acme/api", ghPR{
		Number: 60, HeadRefOid: "status-test-sha", BaseRefName: "main",
	}, h.Cfg)
	if err != nil {
		t.Fatalf("processPR: %v", err)
	}

	if len(statusCalls) != 1 {
		t.Fatalf("expected 1 status call, got %d", len(statusCalls))
	}
	sc := statusCalls[0]
	if sc.repo != "acme/api" {
		t.Errorf("repo=%q, want acme/api", sc.repo)
	}
	if sc.sha != "status-test-sha" {
		t.Errorf("sha=%q, want status-test-sha", sc.sha)
	}
	if sc.state != "pending" {
		t.Errorf("state=%q, want pending", sc.state)
	}
}

func TestCIPollerPostBatchResults_SetsSuccessStatus(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	batch, job := h.seedBatchJob(t, "acme/api", 61, "success-sha", "a..b", "codex", "security")
	h.markJobDoneWithReview(t, job.ID, "codex", "No issues found.")

	type statusCall struct {
		repo, sha, state, desc string
	}
	var statusCalls []statusCall
	h.Poller.setCommitStatusFn = func(repo, sha, state, desc string) error {
		statusCalls = append(statusCalls, statusCall{repo, sha, state, desc})
		return nil
	}
	h.Poller.postPRCommentFn = func(string, int, string) error { return nil }

	h.Poller.handleBatchJobDone(batch, job.ID, true)

	if len(statusCalls) != 1 {
		t.Fatalf("expected 1 status call, got %d", len(statusCalls))
	}
	sc := statusCalls[0]
	if sc.state != "success" {
		t.Errorf("state=%q, want success", sc.state)
	}
	if sc.sha != "success-sha" {
		t.Errorf("sha=%q, want success-sha", sc.sha)
	}
}

func TestCIPollerPostBatchResults_SetsErrorStatusOnAllFailed(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	batch, job := h.seedBatchJob(t, "acme/api", 62, "fail-sha", "a..b", "codex", "security")
	h.markJobFailed(t, job.ID, "timeout")

	type statusCall struct {
		repo, sha, state, desc string
	}
	var statusCalls []statusCall
	h.Poller.setCommitStatusFn = func(repo, sha, state, desc string) error {
		statusCalls = append(statusCalls, statusCall{repo, sha, state, desc})
		return nil
	}
	h.Poller.postPRCommentFn = func(string, int, string) error { return nil }

	h.Poller.handleBatchJobDone(batch, job.ID, false)

	if len(statusCalls) != 1 {
		t.Fatalf("expected 1 status call, got %d", len(statusCalls))
	}
	sc := statusCalls[0]
	if sc.state != "error" {
		t.Errorf("state=%q, want error", sc.state)
	}
	if sc.desc != "All reviews failed" {
		t.Errorf("desc=%q, want 'All reviews failed'", sc.desc)
	}
}

func TestCIPollerPostBatchResults_SetsFailureStatusOnMixedOutcome(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")

	// Create a batch with 2 jobs
	batch, _, err := h.DB.CreateCIBatch("acme/api", 64, "mixed-sha", 2)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}

	// Enqueue two jobs and link them to the batch
	job1, err := h.DB.EnqueueJob(storage.EnqueueOpts{
		RepoID: h.Repo.ID, GitRef: "a..b", Agent: "codex", ReviewType: "security",
	})
	if err != nil {
		t.Fatalf("EnqueueJob 1: %v", err)
	}
	if err := h.DB.RecordBatchJob(batch.ID, job1.ID); err != nil {
		t.Fatalf("RecordBatchJob 1: %v", err)
	}

	job2, err := h.DB.EnqueueJob(storage.EnqueueOpts{
		RepoID: h.Repo.ID, GitRef: "a..b", Agent: "gemini", ReviewType: "review",
	})
	if err != nil {
		t.Fatalf("EnqueueJob 2: %v", err)
	}
	if err := h.DB.RecordBatchJob(batch.ID, job2.ID); err != nil {
		t.Fatalf("RecordBatchJob 2: %v", err)
	}

	// Complete job1, fail job2
	h.markJobDoneWithReview(t, job1.ID, "codex", "No issues found.")
	h.markJobFailed(t, job2.ID, "timeout")

	type statusCall struct {
		repo, sha, state, desc string
	}
	var statusCalls []statusCall
	h.Poller.setCommitStatusFn = func(repo, sha, state, desc string) error {
		statusCalls = append(statusCalls, statusCall{repo, sha, state, desc})
		return nil
	}
	h.Poller.postPRCommentFn = func(string, int, string) error { return nil }

	// First call: job1 succeeded — batch not yet complete
	h.Poller.handleBatchJobDone(batch, job1.ID, true)
	if len(statusCalls) != 0 {
		t.Fatalf("expected no status call after first job, got %d", len(statusCalls))
	}

	// Second call: job2 failed — batch now complete
	h.Poller.handleBatchJobDone(batch, job2.ID, false)
	if len(statusCalls) != 1 {
		t.Fatalf("expected 1 status call after batch complete, got %d", len(statusCalls))
	}

	sc := statusCalls[0]
	if sc.state != "failure" {
		t.Errorf("state=%q, want failure", sc.state)
	}
	if sc.sha != "mixed-sha" {
		t.Errorf("sha=%q, want mixed-sha", sc.sha)
	}
	if !strings.Contains(sc.desc, "1/2 jobs failed") {
		t.Errorf("desc=%q, should mention 1/2 jobs failed", sc.desc)
	}
}

func TestCIPollerPostBatchResults_SetsErrorStatusOnCommentPostFailure(t *testing.T) {
	h := newCIPollerHarness(t, "git@github.com:acme/api.git")
	batch, job := h.seedBatchJob(t, "acme/api", 63, "post-fail-sha", "a..b", "codex", "security")
	h.markJobDoneWithReview(t, job.ID, "codex", "Found issues.")

	type statusCall struct {
		repo, sha, state, desc string
	}
	var statusCalls []statusCall
	h.Poller.setCommitStatusFn = func(repo, sha, state, desc string) error {
		statusCalls = append(statusCalls, statusCall{repo, sha, state, desc})
		return nil
	}
	h.Poller.postPRCommentFn = func(string, int, string) error {
		return context.DeadlineExceeded
	}

	h.Poller.postBatchResults(batch)

	if len(statusCalls) != 1 {
		t.Fatalf("expected 1 status call, got %d", len(statusCalls))
	}
	sc := statusCalls[0]
	if sc.state != "error" {
		t.Errorf("state=%q, want error", sc.state)
	}
	if sc.desc != "Review failed to post" {
		t.Errorf("desc=%q, want 'Review failed to post'", sc.desc)
	}
}
