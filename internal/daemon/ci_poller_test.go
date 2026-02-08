package daemon

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

func TestBuildSynthesisPrompt(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Output: "No issues found.", Status: "done"},
		{JobID: 2, Agent: "gemini", ReviewType: "review", Output: "Consider error handling in foo.go:42", Status: "done"},
		{JobID: 3, Agent: "codex", ReviewType: "review", Status: "failed", Error: "timeout"},
	}

	prompt := buildSynthesisPrompt(reviews)

	// Should contain instructions
	if !strings.Contains(prompt, "Deduplicate findings") {
		t.Error("prompt missing deduplication instruction")
	}
	if !strings.Contains(prompt, "Organize by severity") {
		t.Error("prompt missing severity instruction")
	}

	// Should contain review headers
	if !strings.Contains(prompt, "### Review 1: Agent=codex, Type=security") {
		t.Error("prompt missing review 1 header")
	}
	if !strings.Contains(prompt, "### Review 2: Agent=gemini, Type=review") {
		t.Error("prompt missing review 2 header")
	}
	if !strings.Contains(prompt, "[FAILED: timeout]") {
		t.Error("prompt missing failure annotation")
	}

	// Should contain review outputs
	if !strings.Contains(prompt, "No issues found.") {
		t.Error("prompt missing review 1 output")
	}
	if !strings.Contains(prompt, "foo.go:42") {
		t.Error("prompt missing review 2 output")
	}
	if !strings.Contains(prompt, "(no output") {
		t.Error("prompt missing failed review placeholder")
	}
}

func TestFormatRawBatchComment(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Output: "Finding A", Status: "done"},
		{JobID: 2, Agent: "gemini", ReviewType: "review", Status: "failed", Error: "timeout"},
	}

	comment := formatRawBatchComment(reviews)

	// Header
	if !strings.Contains(comment, "## roborev: Combined Review") {
		t.Error("missing header")
	}
	if !strings.Contains(comment, "Synthesis unavailable") {
		t.Error("missing synthesis unavailable note")
	}

	// Details blocks
	if !strings.Contains(comment, "<details>") {
		t.Error("missing details block")
	}
	if !strings.Contains(comment, "Agent: codex | Type: security | Status: done") {
		t.Error("missing first review summary")
	}
	if !strings.Contains(comment, "Finding A") {
		t.Error("missing first review output")
	}
	if !strings.Contains(comment, "Agent: gemini | Type: review | Status: failed") {
		t.Error("missing second review summary")
	}
	if !strings.Contains(comment, "**Error:** Review failed. Check daemon logs for details.") {
		t.Error("missing error for failed review")
	}
}

func TestFormatSynthesizedComment(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Status: "done"},
		{JobID: 2, Agent: "gemini", ReviewType: "review", Status: "done"},
	}

	output := "All clean. No critical findings."
	comment := formatSynthesizedComment(output, reviews)

	// Header
	if !strings.Contains(comment, "## roborev: Combined Review") {
		t.Error("missing header")
	}

	// Synthesized content
	if !strings.Contains(comment, "All clean. No critical findings.") {
		t.Error("missing synthesized output")
	}

	// Metadata
	if !strings.Contains(comment, "Synthesized from 2 reviews") {
		t.Error("missing metadata")
	}
	if !strings.Contains(comment, "codex") {
		t.Error("missing agent name in metadata")
	}
	if !strings.Contains(comment, "gemini") {
		t.Error("missing agent name in metadata")
	}
	if !strings.Contains(comment, "security") {
		t.Error("missing review type in metadata")
	}
	if !strings.Contains(comment, "review") {
		t.Error("missing review type in metadata")
	}
}

func TestFormatAllFailedComment(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Status: "failed", Error: "timeout"},
		{JobID: 2, Agent: "gemini", ReviewType: "review", Status: "failed", Error: "api error"},
	}

	comment := formatAllFailedComment(reviews)

	if !strings.Contains(comment, "## roborev: Review Failed") {
		t.Error("missing header")
	}
	if !strings.Contains(comment, "All review jobs in this batch failed") {
		t.Error("missing failure message")
	}
	if !strings.Contains(comment, "**codex** (security): failed") {
		t.Error("missing first failure detail")
	}
	if !strings.Contains(comment, "**gemini** (review): failed") {
		t.Error("missing second failure detail")
	}
	if !strings.Contains(comment, "Check daemon logs for error details.") {
		t.Error("missing log reference")
	}
}

func TestGhEnv_FiltersExistingTokens(t *testing.T) {
	// Set up a CIPoller with a pre-cached token (avoids JWT/API calls)
	provider := &GitHubAppTokenProvider{
		token:   "ghs_app_token_123",
		expires: time.Now().Add(1 * time.Hour),
	}
	p := &CIPoller{tokenProvider: provider}

	// Plant GH_TOKEN and GITHUB_TOKEN in env
	t.Setenv("GH_TOKEN", "personal_token")
	t.Setenv("GITHUB_TOKEN", "another_personal_token")

	env := p.ghEnv()

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

func TestGhEnv_NilProvider(t *testing.T) {
	p := &CIPoller{tokenProvider: nil}
	if env := p.ghEnv(); env != nil {
		t.Errorf("expected nil env when no token provider, got %v", env)
	}
}

func TestFormatRawBatchComment_Truncation(t *testing.T) {
	reviews := []storage.BatchReviewResult{
		{JobID: 1, Agent: "codex", ReviewType: "security", Output: strings.Repeat("x", 20000), Status: "done"},
	}

	comment := formatRawBatchComment(reviews)
	if !strings.Contains(comment, "...(truncated)") {
		t.Error("expected truncation for large output")
	}
}

func TestCIPollerProcessPR_EnqueuesMatrix(t *testing.T) {
	db := testutil.OpenTestDB(t)
	repoPath := t.TempDir()
	if _, err := db.GetOrCreateRepo(repoPath, "git@github.com:acme/api.git"); err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.CI.Enabled = true
	cfg.CI.ReviewTypes = []string{"security", "review"}
	cfg.CI.Agents = []string{"codex", "gemini"}
	cfg.CI.Model = "gpt-test"

	p := NewCIPoller(db, NewStaticConfig(cfg), nil)
	p.gitFetchFn = func(context.Context, string) error { return nil }
	p.gitFetchPRHeadFn = func(context.Context, string, int) error { return nil }
	p.mergeBaseFn = func(repoPath, ref1, ref2 string) (string, error) {
		if ref1 != "origin/main" {
			t.Fatalf("merge-base ref1=%q, want origin/main", ref1)
		}
		if ref2 != "head-sha-123" {
			t.Fatalf("merge-base ref2=%q, want head-sha-123", ref2)
		}
		return "base-sha-999", nil
	}

	err := p.processPR(context.Background(), "acme/api", ghPR{
		Number:      42,
		HeadRefOid:  "head-sha-123",
		BaseRefName: "main",
	}, cfg)
	if err != nil {
		t.Fatalf("processPR: %v", err)
	}

	hasBatch, err := db.HasCIBatch("acme/api", 42, "head-sha-123")
	if err != nil {
		t.Fatalf("HasCIBatch: %v", err)
	}
	if !hasBatch {
		t.Fatal("expected CI batch to be created")
	}

	jobs, err := db.ListJobs("", repoPath, 0, 0, storage.WithGitRef("base-sha-999..head-sha-123"))
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
		"codex|review",
		"gemini|security",
		"gemini|review",
	}
	for _, key := range want {
		if !got[key] {
			t.Errorf("missing job combination %q", key)
		}
	}
}

func TestCIPollerPollRepo_UsesPRListAndProcessesEach(t *testing.T) {
	db := testutil.OpenTestDB(t)
	repoPath := t.TempDir()
	if _, err := db.GetOrCreateRepo(repoPath, "https://github.com/acme/api.git"); err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.CI.Enabled = true
	cfg.CI.ReviewType = "security"
	cfg.CI.Agent = "codex"

	p := NewCIPoller(db, NewStaticConfig(cfg), nil)
	p.listOpenPRsFn = func(context.Context, string) ([]ghPR, error) {
		return []ghPR{
			{Number: 7, HeadRefOid: "11111111aaaaaaaa", BaseRefName: "main"},
			{Number: 8, HeadRefOid: "22222222bbbbbbbb", BaseRefName: "main"},
		}, nil
	}
	p.gitFetchFn = func(context.Context, string) error { return nil }
	p.gitFetchPRHeadFn = func(context.Context, string, int) error { return nil }
	p.mergeBaseFn = func(repoPath, ref1, ref2 string) (string, error) {
		return "base-" + ref2, nil
	}

	if err := p.pollRepo(context.Background(), "acme/api", cfg); err != nil {
		t.Fatalf("pollRepo: %v", err)
	}

	hasA, err := db.HasCIBatch("acme/api", 7, "11111111aaaaaaaa")
	if err != nil {
		t.Fatalf("HasCIBatch A: %v", err)
	}
	hasB, err := db.HasCIBatch("acme/api", 8, "22222222bbbbbbbb")
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
	db := testutil.OpenTestDB(t)
	repoPath := t.TempDir()
	repo, err := db.GetOrCreateRepo(repoPath, "git@github.com:acme/api.git")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.CI.Enabled = true
	p := NewCIPoller(db, NewStaticConfig(cfg), nil)

	batch, err := db.CreateCIBatch("acme/api", 1, "sha", 2)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, GitRef: "a..b", Agent: "codex", ReviewType: "security"})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if err := db.RecordBatchJob(batch.ID, job.ID); err != nil {
		t.Fatalf("RecordBatchJob: %v", err)
	}

	posted := 0
	p.postPRCommentFn = func(string, int, string) error {
		posted++
		return nil
	}

	p.handleBatchJobDone(batch, job.ID, true)

	if posted != 0 {
		t.Fatalf("expected no PR comment yet, got %d", posted)
	}
}

func TestCIPollerHandleBatchJobDone_CompleteBatchPostsAndFinalizes(t *testing.T) {
	db := testutil.OpenTestDB(t)
	repoPath := t.TempDir()
	repo, err := db.GetOrCreateRepo(repoPath, "git@github.com:acme/api.git")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.CI.Enabled = true
	p := NewCIPoller(db, NewStaticConfig(cfg), nil)

	batch, err := db.CreateCIBatch("acme/api", 2, "sha", 1)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{
		RepoID:     repo.ID,
		GitRef:     "a..b",
		Agent:      "codex",
		ReviewType: "security",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if err := db.RecordBatchJob(batch.ID, job.ID); err != nil {
		t.Fatalf("RecordBatchJob: %v", err)
	}
	if _, err := db.Exec(`UPDATE review_jobs SET status='done' WHERE id = ?`, job.ID); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO reviews (job_id, agent, prompt, output) VALUES (?, ?, 'p', 'No issues found.')`, job.ID, "codex"); err != nil {
		t.Fatalf("insert review: %v", err)
	}

	var postedRepo string
	var postedPR int
	var postedBody string
	p.postPRCommentFn = func(repo string, pr int, body string) error {
		postedRepo = repo
		postedPR = pr
		postedBody = body
		return nil
	}

	p.handleBatchJobDone(batch, job.ID, true)

	if postedRepo != "acme/api" || postedPR != 2 {
		t.Fatalf("posted to %s#%d, want acme/api#2", postedRepo, postedPR)
	}
	if !strings.Contains(postedBody, "roborev") {
		t.Fatalf("expected roborev comment body, got: %q", postedBody)
	}

	var synthesized int
	var claimedAt sql.NullString
	if err := db.QueryRow(`SELECT synthesized, claimed_at FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&synthesized, &claimedAt); err != nil {
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
	db := testutil.OpenTestDB(t)
	repoPath := t.TempDir()
	repo, err := db.GetOrCreateRepo(repoPath, "https://github.com/acme/api")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.CI.Enabled = true
	p := NewCIPoller(db, NewStaticConfig(cfg), nil)

	batch, err := db.CreateCIBatch("acme/api", 9, "sha", 1)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{RepoID: repo.ID, GitRef: "a..b", Agent: "codex", ReviewType: "security"})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if err := db.RecordBatchJob(batch.ID, job.ID); err != nil {
		t.Fatalf("RecordBatchJob: %v", err)
	}
	if _, err := db.Exec(`UPDATE review_jobs SET status='canceled', error='manual cancel' WHERE id = ?`, job.ID); err != nil {
		t.Fatalf("mark canceled: %v", err)
	}

	var postedBody string
	p.postPRCommentFn = func(_ string, _ int, body string) error {
		postedBody = body
		return nil
	}

	p.reconcileStaleBatches()

	if !strings.Contains(postedBody, "All review jobs in this batch failed.") {
		t.Fatalf("expected all-failed comment, got: %q", postedBody)
	}

	var completed, failed int
	if err := db.QueryRow(`SELECT completed_jobs, failed_jobs FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&completed, &failed); err != nil {
		t.Fatalf("query reconciled counts: %v", err)
	}
	if completed != 0 || failed != 1 {
		t.Fatalf("expected reconciled counts 0/1, got %d/%d", completed, failed)
	}
}

func TestCIPollerHandleReviewCompleted_LegacyCIReviewPostsComment(t *testing.T) {
	db := testutil.OpenTestDB(t)
	repoPath := t.TempDir()
	repo, err := db.GetOrCreateRepo(repoPath, "git@github.com:acme/api.git")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.CI.Enabled = true
	p := NewCIPoller(db, NewStaticConfig(cfg), nil)

	job, err := db.EnqueueJob(storage.EnqueueOpts{
		RepoID:     repo.ID,
		GitRef:     "a..b",
		Agent:      "codex",
		ReviewType: "security",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if _, err := db.Exec(`UPDATE review_jobs SET status='done' WHERE id = ?`, job.ID); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO reviews (job_id, agent, prompt, output) VALUES (?, ?, 'p', 'No issues found.')`, job.ID, "codex"); err != nil {
		t.Fatalf("insert review: %v", err)
	}
	if err := db.RecordCIReview("acme/api", 12, "head-sha", job.ID); err != nil {
		t.Fatalf("RecordCIReview: %v", err)
	}

	called := 0
	p.postPRCommentFn = func(repo string, pr int, body string) error {
		called++
		if repo != "acme/api" || pr != 12 {
			t.Fatalf("posted to %s#%d, want acme/api#12", repo, pr)
		}
		if !strings.Contains(body, "roborev") {
			t.Fatalf("unexpected body: %q", body)
		}
		return nil
	}

	p.handleReviewCompleted(Event{JobID: job.ID, Verdict: "P"})

	if called != 1 {
		t.Fatalf("expected one post call, got %d", called)
	}
}

func TestCIPollerHandleReviewFailed_BatchPath(t *testing.T) {
	db := testutil.OpenTestDB(t)
	repoPath := t.TempDir()
	repo, err := db.GetOrCreateRepo(repoPath, "git@github.com:acme/api.git")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.CI.Enabled = true
	p := NewCIPoller(db, NewStaticConfig(cfg), nil)

	batch, err := db.CreateCIBatch("acme/api", 13, "head-sha", 1)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{
		RepoID:     repo.ID,
		GitRef:     "a..b",
		Agent:      "codex",
		ReviewType: "security",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if err := db.RecordBatchJob(batch.ID, job.ID); err != nil {
		t.Fatalf("RecordBatchJob: %v", err)
	}
	if _, err := db.Exec(`UPDATE review_jobs SET status='failed', error='timeout' WHERE id = ?`, job.ID); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	var postedBody string
	p.postPRCommentFn = func(_ string, _ int, body string) error {
		postedBody = body
		return nil
	}

	p.handleReviewFailed(Event{JobID: job.ID})

	if !strings.Contains(postedBody, "Review Failed") {
		t.Fatalf("expected failure comment, got: %q", postedBody)
	}
}

func TestCIPollerPostBatchResults_SynthesisPathUsesMock(t *testing.T) {
	db := testutil.OpenTestDB(t)
	repoPath := t.TempDir()
	repo, err := db.GetOrCreateRepo(repoPath, "https://github.com/acme/api.git")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.CI.Enabled = true
	p := NewCIPoller(db, NewStaticConfig(cfg), nil)

	batch, err := db.CreateCIBatch("acme/api", 14, "head-sha", 2)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}
	job1, err := db.EnqueueJob(storage.EnqueueOpts{
		RepoID:     repo.ID,
		GitRef:     "a..b",
		Agent:      "codex",
		ReviewType: "security",
	})
	if err != nil {
		t.Fatalf("EnqueueJob job1: %v", err)
	}
	job2, err := db.EnqueueJob(storage.EnqueueOpts{
		RepoID:     repo.ID,
		GitRef:     "a..b",
		Agent:      "gemini",
		ReviewType: "review",
	})
	if err != nil {
		t.Fatalf("EnqueueJob job2: %v", err)
	}
	if err := db.RecordBatchJob(batch.ID, job1.ID); err != nil {
		t.Fatalf("RecordBatchJob job1: %v", err)
	}
	if err := db.RecordBatchJob(batch.ID, job2.ID); err != nil {
		t.Fatalf("RecordBatchJob job2: %v", err)
	}
	if _, err := db.Exec(`UPDATE review_jobs SET status='done' WHERE id = ?`, job1.ID); err != nil {
		t.Fatalf("mark job1 done: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO reviews (job_id, agent, prompt, output) VALUES (?, ?, 'p', 'finding A')`, job1.ID, "codex"); err != nil {
		t.Fatalf("insert review job1: %v", err)
	}
	if _, err := db.Exec(`UPDATE review_jobs SET status='failed', error='timeout' WHERE id = ?`, job2.ID); err != nil {
		t.Fatalf("mark job2 failed: %v", err)
	}

	p.synthesizeFn = func(_ *storage.CIPRBatch, _ []storage.BatchReviewResult, _ *config.Config) (string, error) {
		return "SYNTHESIZED-RESULT", nil
	}

	var postedBody string
	p.postPRCommentFn = func(_ string, _ int, body string) error {
		postedBody = body
		return nil
	}

	p.postBatchResults(batch)

	if !strings.Contains(postedBody, "SYNTHESIZED-RESULT") {
		t.Fatalf("expected synthesized output, got: %q", postedBody)
	}
}

func TestCIPollerPostBatchResults_PostFailureUnclaimsBatch(t *testing.T) {
	db := testutil.OpenTestDB(t)
	repoPath := t.TempDir()
	repo, err := db.GetOrCreateRepo(repoPath, "https://github.com/acme/api.git")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.CI.Enabled = true
	p := NewCIPoller(db, NewStaticConfig(cfg), nil)

	batch, err := db.CreateCIBatch("acme/api", 15, "head-sha", 1)
	if err != nil {
		t.Fatalf("CreateCIBatch: %v", err)
	}
	job, err := db.EnqueueJob(storage.EnqueueOpts{
		RepoID:     repo.ID,
		GitRef:     "a..b",
		Agent:      "codex",
		ReviewType: "security",
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if err := db.RecordBatchJob(batch.ID, job.ID); err != nil {
		t.Fatalf("RecordBatchJob: %v", err)
	}
	if _, err := db.Exec(`UPDATE review_jobs SET status='failed', error='timeout' WHERE id = ?`, job.ID); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	p.postPRCommentFn = func(string, int, string) error {
		return context.DeadlineExceeded
	}

	p.postBatchResults(batch)

	var synthesized int
	if err := db.QueryRow(`SELECT synthesized FROM ci_pr_batches WHERE id = ?`, batch.ID).Scan(&synthesized); err != nil {
		t.Fatalf("query batch: %v", err)
	}
	if synthesized != 0 {
		t.Fatalf("expected batch to be unclaimed (synthesized=0), got %d", synthesized)
	}
}

func TestCIPollerFindLocalRepo_PartialIdentityFallback(t *testing.T) {
	db := testutil.OpenTestDB(t)
	repoPath := t.TempDir()
	repo, err := db.GetOrCreateRepo(repoPath, "ssh://git@github.com/acme/api.git")
	if err != nil {
		t.Fatalf("GetOrCreateRepo: %v", err)
	}

	cfg := config.DefaultConfig()
	p := NewCIPoller(db, NewStaticConfig(cfg), nil)

	found, err := p.findLocalRepo("acme/api")
	if err != nil {
		t.Fatalf("findLocalRepo: %v", err)
	}
	if found.ID != repo.ID {
		t.Fatalf("found repo id %d, want %d", found.ID, repo.ID)
	}
}

func TestCIPollerSynthesizeBatchResults_WithTestAgent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CI.SynthesisAgent = "test"

	p := &CIPoller{}
	out, err := p.synthesizeBatchResults(
		&storage.CIPRBatch{ID: 1},
		[]storage.BatchReviewResult{
			{JobID: 1, Agent: "codex", ReviewType: "security", Output: "No issues found.", Status: "done"},
			{JobID: 2, Agent: "gemini", ReviewType: "review", Output: "Potential bug in x.go:10", Status: "done"},
		},
		cfg,
	)
	if err != nil {
		t.Fatalf("synthesizeBatchResults: %v", err)
	}
	if !strings.Contains(out, "## roborev: Combined Review") {
		t.Fatalf("expected combined review header, got: %q", out)
	}
}
