package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	gitpkg "github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
)

// ghPR represents a GitHub pull request from `gh pr list --json`
type ghPR struct {
	Number      int    `json:"number"`
	HeadRefOid  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
	HeadRefName string `json:"headRefName"`
	Title       string `json:"title"`
}

// CIPoller polls GitHub for open PRs and enqueues security reviews.
// It also listens for review.completed events and posts results as PR comments.
type CIPoller struct {
	db            *storage.DB
	cfgGetter     ConfigGetter
	broadcaster   Broadcaster
	tokenProvider *GitHubAppTokenProvider

	// Test seams for mocking side effects (gh/git/LLM) in unit tests.
	// Nil means use the real implementation.
	listOpenPRsFn     func(context.Context, string) ([]ghPR, error)
	gitFetchFn        func(context.Context, string) error
	gitFetchPRHeadFn  func(context.Context, string, int) error
	mergeBaseFn       func(string, string, string) (string, error)
	postPRCommentFn   func(string, int, string) error
	setCommitStatusFn func(ghRepo, sha, state, description string) error
	synthesizeFn      func(*storage.CIPRBatch, []storage.BatchReviewResult, *config.Config) (string, error)
	agentResolverFn   func(name string) (string, error) // returns resolved agent name
	jobCancelFn       func(jobID int64)                 // kills running worker process (optional)

	subID      int // broadcaster subscription ID for event listening
	stopCh     chan struct{}
	doneCh     chan struct{}
	cancelFunc context.CancelFunc // cancels the context for external commands
	mu         sync.Mutex
	running    bool
}

// NewCIPoller creates a new CI poller.
// If GitHub App is configured, it initializes a token provider so gh commands
// authenticate as the app bot instead of the user's personal account.
func NewCIPoller(db *storage.DB, cfgGetter ConfigGetter, broadcaster Broadcaster) *CIPoller {
	p := &CIPoller{
		db:          db,
		cfgGetter:   cfgGetter,
		broadcaster: broadcaster,
	}
	p.listOpenPRsFn = p.listOpenPRs
	p.gitFetchFn = gitFetchCtx
	p.gitFetchPRHeadFn = gitFetchPRHead
	p.mergeBaseFn = gitpkg.GetMergeBase
	p.postPRCommentFn = p.postPRComment
	p.synthesizeFn = p.synthesizeBatchResults

	cfg := cfgGetter.Config()
	if cfg.CI.GitHubAppConfigured() {
		pemData, err := cfg.CI.GitHubAppPrivateKeyResolved()
		if err != nil {
			log.Printf("CI poller: failed to load GitHub App private key: %v", err)
		} else {
			tp, err := NewGitHubAppTokenProvider(cfg.CI.GitHubAppID, pemData)
			if err != nil {
				log.Printf("CI poller: failed to create GitHub App token provider: %v", err)
			} else {
				p.tokenProvider = tp
				log.Printf("CI poller: GitHub App authentication enabled (app_id=%d)", cfg.CI.GitHubAppID)
			}
		}
	}

	return p
}

// Start begins polling for PRs
func (p *CIPoller) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return fmt.Errorf("CI poller already running")
	}

	cfg := p.cfgGetter.Config()
	if !cfg.CI.Enabled {
		return fmt.Errorf("CI poller not enabled")
	}

	interval, err := time.ParseDuration(cfg.CI.PollInterval)
	if err != nil || interval < 30*time.Second {
		interval = 5 * time.Minute
	}

	ctx, cancel := context.WithCancel(context.Background())

	p.stopCh = make(chan struct{})
	p.doneCh = make(chan struct{})
	p.cancelFunc = cancel
	p.running = true

	stopCh := p.stopCh
	doneCh := p.doneCh

	// Subscribe to events before starting poll to avoid missing early completions
	if p.broadcaster != nil {
		subID, eventCh := p.broadcaster.Subscribe("")
		p.subID = subID
		go p.listenForEvents(stopCh, eventCh)
	}

	go p.run(ctx, stopCh, doneCh, interval)

	return nil
}

// Stop gracefully shuts down the CI poller
func (p *CIPoller) Stop() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	stopCh := p.stopCh
	doneCh := p.doneCh
	cancel := p.cancelFunc
	p.running = false
	p.mu.Unlock()

	cancel() // Cancel context for external commands
	close(stopCh)
	<-doneCh

	if p.broadcaster != nil && p.subID != 0 {
		p.broadcaster.Unsubscribe(p.subID)
	}
}

// HealthCheck returns whether the CI poller is healthy
func (p *CIPoller) HealthCheck() (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return false, "not running"
	}
	return true, "running"
}

func (p *CIPoller) run(ctx context.Context, stopCh, doneCh chan struct{}, interval time.Duration) {
	defer close(doneCh)

	// Poll immediately on start
	p.poll(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			log.Println("CI poller stopped")
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p *CIPoller) poll(ctx context.Context) {
	cfg := p.cfgGetter.Config()
	for _, ghRepo := range cfg.CI.Repos {
		if err := p.pollRepo(ctx, ghRepo, cfg); err != nil {
			log.Printf("CI poller: error polling %s: %v", ghRepo, err)
		}
	}

	// Reconcile stale batches where events may have been dropped.
	// This catches batches where all jobs are terminal but the event-driven
	// counters fell behind (e.g., broadcaster dropped events, or canceled jobs).
	p.reconcileStaleBatches()
}

func (p *CIPoller) pollRepo(ctx context.Context, ghRepo string, cfg *config.Config) error {
	// List open PRs via gh CLI
	prs, err := p.callListOpenPRs(ctx, ghRepo)
	if err != nil {
		return fmt.Errorf("list PRs: %w", err)
	}

	for _, pr := range prs {
		if err := p.processPR(ctx, ghRepo, pr, cfg); err != nil {
			log.Printf("CI poller: error processing %s#%d: %v", ghRepo, pr.Number, err)
		}
	}
	return nil
}

func (p *CIPoller) processPR(ctx context.Context, ghRepo string, pr ghPR, cfg *config.Config) error {
	// Check if already reviewed at this HEAD SHA (batch takes priority over legacy)
	hasBatch, err := p.db.HasCIBatch(ghRepo, pr.Number, pr.HeadRefOid)
	if err != nil {
		return fmt.Errorf("check CI batch: %w", err)
	}
	if hasBatch {
		return nil
	}

	// Also check legacy single-review table for backward compatibility
	reviewed, err := p.db.HasCIReview(ghRepo, pr.Number, pr.HeadRefOid)
	if err != nil {
		return fmt.Errorf("check CI review: %w", err)
	}
	if reviewed {
		return nil
	}

	// Find local repo matching this GitHub repo
	repo, err := p.findLocalRepo(ghRepo)
	if err != nil {
		return fmt.Errorf("find local repo for %s: %w", ghRepo, err)
	}

	// Fetch latest refs and the PR head (which may come from a fork
	// and not be reachable via a normal fetch).
	if err := p.callGitFetch(ctx, repo.RootPath); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}
	if err := p.callGitFetchPRHead(ctx, repo.RootPath, pr.Number); err != nil {
		log.Printf("CI poller: warning: could not fetch PR head for %s#%d: %v", ghRepo, pr.Number, err)
		// Continue anyway — head commit may already be available from a normal fetch
	}

	// Determine merge base
	baseRef := "origin/" + pr.BaseRefName
	mergeBase, err := p.callMergeBase(repo.RootPath, baseRef, pr.HeadRefOid)
	if err != nil {
		return fmt.Errorf("merge-base %s %s: %w", baseRef, pr.HeadRefOid, err)
	}

	// Build git ref for range review
	gitRef := mergeBase + ".." + pr.HeadRefOid

	// Resolve review types, agents, and reasoning from config.
	// Per-repo CI overrides take priority over global CI config.
	reviewTypes := cfg.CI.ResolvedReviewTypes()
	agents := cfg.CI.ResolvedAgents()
	reasoning := "thorough"

	repoCfg, repoCfgErr := loadCIRepoConfig(repo.RootPath)
	if repoCfgErr != nil {
		log.Printf("CI poller: warning: failed to load repo config for %s: %v", ghRepo, repoCfgErr)
	}
	if repoCfg != nil {
		if len(repoCfg.CI.ReviewTypes) > 0 {
			reviewTypes = repoCfg.CI.ReviewTypes
		}
		if len(repoCfg.CI.Agents) > 0 {
			agents = repoCfg.CI.Agents
		}
		if strings.TrimSpace(repoCfg.CI.Reasoning) != "" {
			if r, err := config.NormalizeReasoning(repoCfg.CI.Reasoning); err == nil && r != "" {
				reasoning = r
			} else if err != nil {
				log.Printf("CI poller: invalid reasoning %q in repo config for %s, using default", repoCfg.CI.Reasoning, ghRepo)
			}
		}
	}

	// Validate, canonicalize, and dedupe review types.
	// Empty string is rejected here (likely a config typo); use "default" explicitly.
	validSpecialTypes := map[string]bool{"security": true, "design": true}
	seen := make(map[string]bool, len(reviewTypes))
	canonical := make([]string, 0, len(reviewTypes))
	for _, rt := range reviewTypes {
		if rt == "" {
			return fmt.Errorf("invalid review_type %q (valid: default, security, design)", rt)
		}
		if !config.IsDefaultReviewType(rt) && !validSpecialTypes[rt] {
			return fmt.Errorf("invalid review_type %q (valid: default, security, design)", rt)
		}
		// Normalize aliases to canonical "default"
		if config.IsDefaultReviewType(rt) {
			rt = "default"
		}
		if !seen[rt] {
			seen[rt] = true
			canonical = append(canonical, rt)
		}
	}
	reviewTypes = canonical

	totalJobs := len(reviewTypes) * len(agents)

	// Cancel any in-progress batches for this PR at an older HEAD SHA.
	// When a PR gets a new push, work on the old HEAD is wasted.
	if canceledIDs, err := p.db.CancelSupersededBatches(ghRepo, pr.Number, pr.HeadRefOid); err != nil {
		log.Printf("CI poller: error canceling superseded batches for %s#%d: %v", ghRepo, pr.Number, err)
	} else if len(canceledIDs) > 0 {
		headShort := pr.HeadRefOid
		if len(headShort) > 8 {
			headShort = headShort[:8]
		}
		log.Printf("CI poller: canceled %d superseded jobs for %s#%d (new HEAD=%s)",
			len(canceledIDs), ghRepo, pr.Number, headShort)
		// Also kill running worker processes so they stop consuming compute.
		if p.jobCancelFn != nil {
			for _, jid := range canceledIDs {
				p.jobCancelFn(jid)
			}
		}
	}

	// Create batch — only the creator proceeds to enqueue (prevents race)
	batch, created, err := p.db.CreateCIBatch(ghRepo, pr.Number, pr.HeadRefOid, totalJobs)
	if err != nil {
		return fmt.Errorf("create CI batch: %w", err)
	}
	if !created {
		// Batch already exists — check if it's fully populated.
		// If the creator crashed mid-enqueue, the batch may have fewer
		// linked jobs than expected. Only reclaim stale batches (>1 min)
		// to avoid racing with an actively enqueuing creator.
		linked, err := p.db.CountBatchJobs(batch.ID)
		if err != nil {
			return fmt.Errorf("count batch jobs: %w", err)
		}
		if linked < batch.TotalJobs {
			stale, err := p.db.IsBatchStale(batch.ID)
			if err != nil {
				return fmt.Errorf("check batch staleness: %w", err)
			}
			if !stale {
				// Batch is still fresh — creator may still be enqueuing
				return nil
			}
			log.Printf("CI poller: batch %d has %d/%d linked jobs (stale incomplete), cleaning up for retry",
				batch.ID, linked, batch.TotalJobs)
			// Cancel any already-linked jobs before deleting the batch.
			// Abort reclaim on real DB errors (sql.ErrNoRows means the
			// job is already terminal, which is fine).
			jobIDs, err := p.db.GetBatchJobIDs(batch.ID)
			if err != nil {
				return fmt.Errorf("get batch job IDs: %w", err)
			}
			for _, jid := range jobIDs {
				if err := p.db.CancelJob(jid); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						continue // already terminal
					}
					return fmt.Errorf("cancel orphan job %d: %w", jid, err)
				}
			}
			if err := p.db.DeleteCIBatch(batch.ID); err != nil {
				return fmt.Errorf("clean up incomplete batch: %w", err)
			}
			// Re-create the batch — this time we're the creator
			batch, created, err = p.db.CreateCIBatch(ghRepo, pr.Number, pr.HeadRefOid, totalJobs)
			if err != nil {
				return fmt.Errorf("re-create CI batch: %w", err)
			}
			if !created {
				// Another poller beat us again — let them handle it
				return nil
			}
		} else {
			// Fully populated batch — nothing to do
			return nil
		}
	}

	// Enqueue jobs for each review_type x agent combination.
	// If any enqueue fails, cancel already-created jobs and delete the batch
	// so the next poll can retry cleanly.
	var createdJobIDs []int64
	rollback := func() {
		for _, jid := range createdJobIDs {
			if err := p.db.CancelJob(jid); err != nil {
				log.Printf("CI poller: failed to cancel orphan job %d: %v", jid, err)
			}
		}
		if err := p.db.DeleteCIBatch(batch.ID); err != nil {
			log.Printf("CI poller: failed to clean up batch %d: %v", batch.ID, err)
		}
	}

	for _, rt := range reviewTypes {
		// Map review_type to workflow name (same as handleEnqueue).
		workflow := "review"
		if !config.IsDefaultReviewType(rt) {
			workflow = rt
		}

		for _, ag := range agents {
			// Resolve agent through workflow config when not explicitly set
			resolvedAgent := config.ResolveAgentForWorkflow(ag, repo.RootPath, cfg, workflow, reasoning)
			if p.agentResolverFn != nil {
				name, err := p.agentResolverFn(resolvedAgent)
				if err != nil {
					rollback()
					return fmt.Errorf("no review agent available for type=%s: %w", rt, err)
				}
				resolvedAgent = name
			} else if resolved, err := agent.GetAvailable(resolvedAgent); err != nil {
				rollback()
				return fmt.Errorf("no review agent available for type=%s: %w", rt, err)
			} else {
				resolvedAgent = resolved.Name()
			}

			// Resolve model through workflow config when not explicitly set
			resolvedModel := config.ResolveModelForWorkflow(cfg.CI.Model, repo.RootPath, cfg, workflow, reasoning)

			job, err := p.db.EnqueueJob(storage.EnqueueOpts{
				RepoID:     repo.ID,
				GitRef:     gitRef,
				Agent:      resolvedAgent,
				Model:      resolvedModel,
				Reasoning:  reasoning,
				ReviewType: rt,
			})
			if err != nil {
				rollback()
				return fmt.Errorf("enqueue job (type=%s, agent=%s): %w", rt, resolvedAgent, err)
			}
			createdJobIDs = append(createdJobIDs, job.ID)

			if err := p.db.RecordBatchJob(batch.ID, job.ID); err != nil {
				rollback()
				return fmt.Errorf("record batch job: %w", err)
			}

			log.Printf("CI poller: enqueued job %d for %s#%d (type=%s, agent=%s, range=%s)",
				job.ID, ghRepo, pr.Number, rt, resolvedAgent, gitRef)
		}
	}

	headShort := pr.HeadRefOid
	if len(headShort) > 8 {
		headShort = headShort[:8]
	}
	log.Printf("CI poller: created batch %d for %s#%d (HEAD=%s, %d jobs)",
		batch.ID, ghRepo, pr.Number, headShort, totalJobs)

	if err := p.callSetCommitStatus(ghRepo, pr.HeadRefOid, "pending", "Review in progress"); err != nil {
		log.Printf("CI poller: failed to set pending status for %s@%s: %v", ghRepo, headShort, err)
	}

	return nil
}

// findLocalRepo finds the local repo that corresponds to a GitHub "owner/repo" identifier.
// It looks for repos whose identity contains the owner/repo pattern.
// Matching is case-insensitive since GitHub owner/repo names are case-insensitive.
func (p *CIPoller) findLocalRepo(ghRepo string) (*storage.Repo, error) {
	// Try common identity patterns (case-insensitive via DB query):
	// - git@github.com:owner/repo.git
	// - https://github.com/owner/repo.git
	// - https://github.com/owner/repo
	lower := strings.ToLower(ghRepo)
	patterns := []string{
		"git@github.com:" + lower + ".git",
		"https://github.com/" + lower + ".git",
		"https://github.com/" + lower,
	}

	for _, pattern := range patterns {
		repo, err := p.db.GetRepoByIdentityCaseInsensitive(pattern)
		if err != nil {
			// Propagate ambiguity errors (e.g., multiple repos with same identity)
			if strings.Contains(err.Error(), "multiple repos") {
				return nil, fmt.Errorf("ambiguous repo match for %q: %w", ghRepo, err)
			}
			continue // Other errors (DB issues) — try next pattern
		}
		if repo != nil {
			// Skip sync placeholders (root_path == identity) — they don't
			// have a real checkout the poller can git-fetch or review.
			if repo.RootPath == repo.Identity {
				continue
			}
			return repo, nil
		}
	}

	// Fall back: search all repos and check if identity ends with owner/repo
	return p.findRepoByPartialIdentity(ghRepo)
}

// findRepoByPartialIdentity searches repos for a matching GitHub owner/repo pattern.
// Matching is case-insensitive since GitHub owner/repo names are case-insensitive.
// Returns an ambiguity error if multiple repos match.
func (p *CIPoller) findRepoByPartialIdentity(ghRepo string) (*storage.Repo, error) {
	rows, err := p.db.Query(`SELECT id, root_path, name, identity FROM repos WHERE identity IS NOT NULL AND identity != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Normalize the search pattern: owner/repo (without .git), lowercased
	needle := strings.ToLower(strings.TrimSuffix(ghRepo, ".git"))

	var matches []storage.Repo
	for rows.Next() {
		var repo storage.Repo
		var identity string
		if err := rows.Scan(&repo.ID, &repo.RootPath, &repo.Name, &identity); err != nil {
			continue
		}
		// Skip sync placeholders (root_path == identity)
		if repo.RootPath == identity {
			continue
		}
		// Check if identity contains the owner/repo pattern (case-insensitive)
		// Strip .git suffix for comparison
		normalized := strings.ToLower(strings.TrimSuffix(identity, ".git"))
		if strings.HasSuffix(normalized, "/"+needle) || strings.HasSuffix(normalized, ":"+needle) {
			repo.Identity = identity
			matches = append(matches, repo)
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no local repo found matching %q (run 'roborev init' in a local checkout)", ghRepo)
	case 1:
		return &matches[0], nil
	default:
		return nil, fmt.Errorf("ambiguous repo match for %q: %d local repos match (partial identity)", ghRepo, len(matches))
	}
}

// ghEnvForRepo returns the environment for gh CLI commands targeting a specific repo.
// It resolves the installation ID for the repo's owner and injects GH_TOKEN.
// Returns nil if no token provider, no installation ID for the owner, or on error
// (gh uses its default auth in those cases).
func (p *CIPoller) ghEnvForRepo(ghRepo string) []string {
	if p.tokenProvider == nil {
		return nil
	}
	// Extract owner from "owner/repo"
	owner, _, _ := strings.Cut(ghRepo, "/")
	cfg := p.cfgGetter.Config()
	installationID := cfg.CI.InstallationIDForOwner(owner)
	if installationID == 0 {
		log.Printf("CI poller: no installation ID for owner %q, using default gh auth", owner)
		return nil
	}
	token, err := p.tokenProvider.TokenForInstallation(installationID)
	if err != nil {
		log.Printf("CI poller: WARNING: GitHub App token failed for %q, falling back to default gh auth: %v", owner, err)
		return nil
	}
	// Filter out any existing GH_TOKEN or GITHUB_TOKEN to ensure our
	// app token takes precedence over the user's personal token.
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GH_TOKEN=") || strings.HasPrefix(e, "GITHUB_TOKEN=") {
			continue
		}
		env = append(env, e)
	}
	return append(env, "GH_TOKEN="+token)
}

// listOpenPRs uses the gh CLI to list open PRs for a GitHub repo
func (p *CIPoller) listOpenPRs(ctx context.Context, ghRepo string) ([]ghPR, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"--repo", ghRepo,
		"--json", "number,headRefOid,baseRefName,headRefName,title",
		"--state", "open",
		"--limit", "100",
	)
	if env := p.ghEnvForRepo(ghRepo); env != nil {
		cmd.Env = env
	}
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh pr list: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("gh pr list: %w", err)
	}

	var prs []ghPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse gh output: %w", err)
	}
	return prs, nil
}

// gitFetchCtx runs git fetch in the repo with context for cancellation.
func gitFetchCtx(ctx context.Context, repoPath string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "fetch", "--quiet")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", err, string(out))
	}
	return nil
}

// gitFetchPRHead fetches the head commit for a GitHub PR. This is needed
// for fork-based PRs where the head commit isn't in the normal fetch refs.
func gitFetchPRHead(ctx context.Context, repoPath string, prNumber int) error {
	ref := fmt.Sprintf("pull/%d/head", prNumber)
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "fetch", "origin", ref, "--quiet")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", err, string(out))
	}
	return nil
}

// listenForEvents subscribes to broadcaster events and posts PR comments
// when CI-triggered reviews complete or fail.
func (p *CIPoller) listenForEvents(stopCh chan struct{}, eventCh <-chan Event) {
	for {
		select {
		case <-stopCh:
			return
		case event, ok := <-eventCh:
			if !ok {
				return
			}
			switch event.Type {
			case "review.completed":
				p.handleReviewCompleted(event)
			case "review.failed", "review.canceled":
				p.handleReviewFailed(event)
			}
		}
	}
}

// handleReviewCompleted checks if a completed review is part of a batch
// and posts results when the batch is complete.
func (p *CIPoller) handleReviewCompleted(event Event) {
	// Try batch flow first
	batch, err := p.db.GetCIBatchByJobID(event.JobID)
	if err != nil {
		log.Printf("CI poller: error checking CI batch for job %d: %v", event.JobID, err)
		return
	}
	if batch != nil {
		p.handleBatchJobDone(batch, event.JobID, true)
		return
	}

	// Fall back to legacy single-review flow
	ciReview, err := p.db.GetCIReviewByJobID(event.JobID)
	if err != nil {
		log.Printf("CI poller: error checking CI review for job %d: %v", event.JobID, err)
		return
	}
	if ciReview == nil {
		return // Not a CI-triggered review
	}

	// Get the full review output
	review, err := p.db.GetReviewByJobID(event.JobID)
	if err != nil {
		log.Printf("CI poller: error getting review for job %d: %v", event.JobID, err)
		return
	}

	// Format and post the comment
	comment := formatPRComment(review, event.Verdict)
	if err := p.callPostPRComment(ciReview.GithubRepo, ciReview.PRNumber, comment); err != nil {
		log.Printf("CI poller: error posting PR comment for %s#%d: %v",
			ciReview.GithubRepo, ciReview.PRNumber, err)
		return
	}

	log.Printf("CI poller: posted review comment on %s#%d (job %d, verdict=%s)",
		ciReview.GithubRepo, ciReview.PRNumber, event.JobID, event.Verdict)
}

// handleReviewFailed handles a failed review job that may be part of a batch.
func (p *CIPoller) handleReviewFailed(event Event) {
	batch, err := p.db.GetCIBatchByJobID(event.JobID)
	if err != nil {
		log.Printf("CI poller: error checking CI batch for failed job %d: %v", event.JobID, err)
		return
	}
	if batch == nil {
		return // Not part of a batch
	}
	p.handleBatchJobDone(batch, event.JobID, false)
}

// handleBatchJobDone processes a completed or failed job within a batch.
// When all jobs are done, it posts the combined results.
func (p *CIPoller) handleBatchJobDone(batch *storage.CIPRBatch, jobID int64, success bool) {
	var updated *storage.CIPRBatch
	var err error
	if success {
		updated, err = p.db.IncrementBatchCompleted(batch.ID)
	} else {
		updated, err = p.db.IncrementBatchFailed(batch.ID)
	}
	if err != nil {
		log.Printf("CI poller: error updating batch %d for job %d: %v", batch.ID, jobID, err)
		return
	}

	// Check if all jobs are done
	if updated.CompletedJobs+updated.FailedJobs < updated.TotalJobs {
		log.Printf("CI poller: batch %d progress: %d/%d completed, %d failed (job %d)",
			updated.ID, updated.CompletedJobs, updated.TotalJobs, updated.FailedJobs, jobID)
		return
	}

	// Guard against duplicate synthesis
	if updated.Synthesized {
		return
	}

	log.Printf("CI poller: batch %d complete (%d succeeded, %d failed), posting results",
		updated.ID, updated.CompletedJobs, updated.FailedJobs)

	p.postBatchResults(updated)
}

// reconcileStaleBatches finds batches where all linked jobs are terminal
// but the event-driven counters are behind (due to dropped events or
// unhandled terminal states), corrects the counts from DB state, and
// triggers synthesis if the batch is now complete.
func (p *CIPoller) reconcileStaleBatches() {
	// Clean up empty batches left by daemon crashes during enqueue.
	if n, err := p.db.DeleteEmptyBatches(); err != nil {
		log.Printf("CI poller: error cleaning empty batches: %v", err)
	} else if n > 0 {
		log.Printf("CI poller: cleaned up %d empty batches", n)
	}

	batches, err := p.db.GetStaleBatches()
	if err != nil {
		log.Printf("CI poller: error checking stale batches: %v", err)
		return
	}

	for _, batch := range batches {
		log.Printf("CI poller: reconciling stale batch %d for %s#%d (counters: %d+%d/%d)",
			batch.ID, batch.GithubRepo, batch.PRNumber,
			batch.CompletedJobs, batch.FailedJobs, batch.TotalJobs)

		updated, err := p.db.ReconcileBatch(batch.ID)
		if err != nil {
			log.Printf("CI poller: error reconciling batch %d: %v", batch.ID, err)
			continue
		}

		if updated.CompletedJobs+updated.FailedJobs >= updated.TotalJobs {
			if updated.Synthesized {
				// Stale claim: daemon crashed mid-post. Unclaim so
				// postBatchResults can re-claim via CAS.
				log.Printf("CI poller: unclaiming stale batch %d (claimed_at expired)", updated.ID)
				if err := p.db.UnclaimBatch(updated.ID); err != nil {
					log.Printf("CI poller: error unclaiming batch %d: %v", batch.ID, err)
					continue
				}
			}
			log.Printf("CI poller: batch %d reconciled (%d succeeded, %d failed), posting results",
				updated.ID, updated.CompletedJobs, updated.FailedJobs)
			p.postBatchResults(updated)
		}
	}
}

// postBatchResults gathers all review outputs for a batch and posts a combined PR comment.
// Uses CAS to atomically claim the batch before posting, preventing duplicate comments
// when event handlers and the reconciler race on the same batch.
func (p *CIPoller) postBatchResults(batch *storage.CIPRBatch) {
	// Atomically claim this batch. If another goroutine already claimed it, skip.
	claimed, err := p.db.ClaimBatchForSynthesis(batch.ID)
	if err != nil {
		log.Printf("CI poller: error claiming batch %d for synthesis: %v", batch.ID, err)
		return
	}
	if !claimed {
		return
	}

	reviews, err := p.db.GetBatchReviews(batch.ID)
	if err != nil {
		log.Printf("CI poller: error getting batch reviews for batch %d: %v", batch.ID, err)
		p.unclaimBatch(batch.ID)
		return
	}

	var comment string
	successCount := 0
	for _, r := range reviews {
		if r.Status == "done" {
			successCount++
		}
	}

	if batch.TotalJobs == 1 && successCount == 1 {
		// Single job batch — use legacy format (no synthesis needed)
		review, err := p.db.GetReviewByJobID(reviews[0].JobID)
		if err != nil {
			log.Printf("CI poller: error getting review for job %d: %v", reviews[0].JobID, err)
			p.unclaimBatch(batch.ID)
			return
		}
		verdict := ""
		if review.Job != nil && review.Job.Verdict != nil {
			verdict = *review.Job.Verdict
		}
		comment = formatPRComment(review, verdict)
	} else if successCount == 0 {
		// All jobs failed — post raw error comment
		comment = formatAllFailedComment(reviews, batch.HeadSHA)
	} else {
		// Multiple jobs — try synthesis
		cfg := p.cfgGetter.Config()
		synthesized, err := p.callSynthesize(batch, reviews, cfg)
		if err != nil {
			log.Printf("CI poller: synthesis failed for batch %d: %v (falling back to raw)", batch.ID, err)
			comment = formatRawBatchComment(reviews, batch.HeadSHA)
		} else {
			comment = synthesized
		}
	}

	if err := p.callPostPRComment(batch.GithubRepo, batch.PRNumber, comment); err != nil {
		log.Printf("CI poller: error posting batch comment for %s#%d: %v",
			batch.GithubRepo, batch.PRNumber, err)
		if err := p.callSetCommitStatus(batch.GithubRepo, batch.HeadSHA, "error", "Review failed to post"); err != nil {
			log.Printf("CI poller: failed to set error status for %s@%s: %v", batch.GithubRepo, batch.HeadSHA, err)
		}
		// Release claim so reconciler can retry
		p.unclaimBatch(batch.ID)
		return
	}

	// Set commit status based on job outcomes:
	//   all succeeded  → success
	//   mixed          → failure (some reviews did not complete)
	//   all failed     → error
	statusState := "success"
	statusDesc := "Review complete"
	switch {
	case batch.CompletedJobs == 0:
		statusState = "error"
		statusDesc = "All reviews failed"
	case batch.FailedJobs > 0:
		statusState = "failure"
		statusDesc = fmt.Sprintf(
			"Review complete (%d/%d jobs failed)",
			batch.FailedJobs, batch.TotalJobs,
		)
	}
	if err := p.callSetCommitStatus(batch.GithubRepo, batch.HeadSHA, statusState, statusDesc); err != nil {
		log.Printf("CI poller: failed to set %s status for %s@%s: %v", statusState, batch.GithubRepo, batch.HeadSHA, err)
	}

	// Clear claimed_at to mark as successfully posted. This prevents
	// GetStaleBatches from re-picking this batch after the 5-min timeout.
	if err := p.db.FinalizeBatch(batch.ID); err != nil {
		log.Printf("CI poller: warning: failed to finalize batch %d: %v", batch.ID, err)
	}

	log.Printf("CI poller: posted batch comment on %s#%d (batch %d, %d reviews)",
		batch.GithubRepo, batch.PRNumber, batch.ID, len(reviews))
}

// unclaimBatch resets the synthesized flag so the batch can be retried.
func (p *CIPoller) unclaimBatch(batchID int64) {
	if err := p.db.UnclaimBatch(batchID); err != nil {
		log.Printf("CI poller: error unclaiming batch %d: %v", batchID, err)
	}
}

// resolveRepoForBatch looks up the local repo associated with a batch's GitHub repo.
// Returns nil if the repo can't be found (synthesis proceeds without per-repo overrides).
func (p *CIPoller) resolveRepoForBatch(batch *storage.CIPRBatch) *storage.Repo {
	if p.db == nil || batch.GithubRepo == "" {
		return nil
	}
	repo, err := p.findLocalRepo(batch.GithubRepo)
	if err != nil {
		log.Printf("CI poller: could not resolve local repo for %s: %v (per-repo overrides will not apply)", batch.GithubRepo, err)
		return nil
	}
	return repo
}

// loadCIRepoConfig loads .roborev.toml from the repo's default branch
// (e.g., origin/main) rather than the working tree. This ensures the CI
// poller uses current settings even when the local checkout is stale.
// Falls back to the filesystem only if the default branch has no config
// file. Parse errors and other failures are returned, not masked.
func loadCIRepoConfig(repoPath string) (*config.RepoConfig, error) {
	defaultBranch, err := gitpkg.GetDefaultBranch(repoPath)
	if err != nil {
		// Can't determine default branch (no origin, bare repo, etc.)
		// — fall back to filesystem.
		return config.LoadRepoConfig(repoPath)
	}

	cfg, err := config.LoadRepoConfigFromRef(repoPath, defaultBranch)
	if err != nil {
		// Config exists but is invalid — surface the error, don't
		// silently fall back to a stale working-tree copy.
		return nil, err
	}
	if cfg != nil {
		return cfg, nil
	}
	// No .roborev.toml on the default branch — fall back to filesystem.
	return config.LoadRepoConfig(repoPath)
}

// resolveMinSeverity determines the effective min_severity for synthesis.
// Priority: per-repo .roborev.toml [ci] min_severity > global [ci] min_severity > "" (no filter).
// Invalid values are logged and skipped.
func resolveMinSeverity(globalMinSeverity, repoPath, ghRepo string) string {
	minSeverity := globalMinSeverity

	// Try per-repo override (from default branch, not working tree)
	if repoPath != "" {
		repoCfg, err := loadCIRepoConfig(repoPath)
		if err != nil {
			log.Printf("CI poller: failed to load repo config from %s: %v (using global min_severity)", repoPath, err)
		} else if repoCfg != nil {
			if s := strings.TrimSpace(repoCfg.CI.MinSeverity); s != "" {
				if normalized, err := config.NormalizeMinSeverity(s); err == nil {
					minSeverity = normalized
				} else {
					log.Printf("CI poller: invalid min_severity %q in repo config for %s, using global", s, ghRepo)
				}
			}
		}
	}

	// Normalize (handles the global value or already-normalized repo value)
	if normalized, err := config.NormalizeMinSeverity(minSeverity); err == nil {
		return normalized
	}
	log.Printf("CI poller: invalid global min_severity %q, ignoring", minSeverity)
	return ""
}

// synthesizeBatchResults uses an LLM agent to combine multiple review outputs.
func (p *CIPoller) synthesizeBatchResults(batch *storage.CIPRBatch, reviews []storage.BatchReviewResult, cfg *config.Config) (string, error) {
	synthesisAgent, err := agent.GetAvailable(cfg.CI.SynthesisAgent)
	if err != nil {
		return "", fmt.Errorf("get synthesis agent: %w", err)
	}

	if cfg.CI.SynthesisModel != "" {
		synthesisAgent = synthesisAgent.WithModel(cfg.CI.SynthesisModel)
	}

	// Resolve repo for per-repo overrides and as the working directory
	// for the synthesis agent (agents like codex require a git repo).
	var repoPath string
	if repo := p.resolveRepoForBatch(batch); repo != nil {
		repoPath = repo.RootPath
	}

	minSeverity := resolveMinSeverity(cfg.CI.MinSeverity, repoPath, batch.GithubRepo)
	prompt := buildSynthesisPrompt(reviews, minSeverity)

	// Run synthesis from the repo's checkout directory so agents that
	// require a git working tree (e.g. codex) don't fail.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	output, err := synthesisAgent.Review(ctx, repoPath, "", prompt, nil)
	if err != nil {
		return "", fmt.Errorf("synthesis review: %w", err)
	}

	return formatSynthesizedComment(output, reviews, batch.HeadSHA), nil
}

func (p *CIPoller) callListOpenPRs(ctx context.Context, ghRepo string) ([]ghPR, error) {
	if p.listOpenPRsFn != nil {
		return p.listOpenPRsFn(ctx, ghRepo)
	}
	return p.listOpenPRs(ctx, ghRepo)
}

func (p *CIPoller) callGitFetch(ctx context.Context, repoPath string) error {
	if p.gitFetchFn != nil {
		return p.gitFetchFn(ctx, repoPath)
	}
	return gitFetchCtx(ctx, repoPath)
}

func (p *CIPoller) callGitFetchPRHead(ctx context.Context, repoPath string, prNumber int) error {
	if p.gitFetchPRHeadFn != nil {
		return p.gitFetchPRHeadFn(ctx, repoPath, prNumber)
	}
	return gitFetchPRHead(ctx, repoPath, prNumber)
}

func (p *CIPoller) callMergeBase(repoPath, baseRef, headRef string) (string, error) {
	if p.mergeBaseFn != nil {
		return p.mergeBaseFn(repoPath, baseRef, headRef)
	}
	return gitpkg.GetMergeBase(repoPath, baseRef, headRef)
}

func (p *CIPoller) callPostPRComment(ghRepo string, prNumber int, body string) error {
	if p.postPRCommentFn != nil {
		return p.postPRCommentFn(ghRepo, prNumber, body)
	}
	return p.postPRComment(ghRepo, prNumber, body)
}

func (p *CIPoller) callSynthesize(batch *storage.CIPRBatch, reviews []storage.BatchReviewResult, cfg *config.Config) (string, error) {
	if p.synthesizeFn != nil {
		return p.synthesizeFn(batch, reviews, cfg)
	}
	return p.synthesizeBatchResults(batch, reviews, cfg)
}

func (p *CIPoller) callSetCommitStatus(ghRepo, sha, state, description string) error {
	if p.setCommitStatusFn != nil {
		return p.setCommitStatusFn(ghRepo, sha, state, description)
	}
	return p.setCommitStatus(ghRepo, sha, state, description)
}

// setCommitStatus posts a commit status check via the GitHub API.
// Uses the GitHub App token provider for authentication. If no token
// provider is configured, the call is silently skipped.
func (p *CIPoller) setCommitStatus(ghRepo, sha, state, description string) error {
	if p.tokenProvider == nil {
		return nil
	}

	owner, _, _ := strings.Cut(ghRepo, "/")
	cfg := p.cfgGetter.Config()
	installationID := cfg.CI.InstallationIDForOwner(owner)
	if installationID == 0 {
		return nil
	}

	path := fmt.Sprintf("/repos/%s/statuses/%s", ghRepo, sha)
	payload := fmt.Sprintf(
		`{"state":%q,"description":%q,"context":"roborev"}`,
		state, description,
	)
	body := strings.NewReader(payload)

	resp, err := p.tokenProvider.APIRequest("POST", path, body, installationID)
	if err != nil {
		return fmt.Errorf("set commit status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf(
				"set commit status: HTTP %d (body unreadable: %v)",
				resp.StatusCode, readErr,
			)
		}
		return fmt.Errorf(
			"set commit status: HTTP %d: %s",
			resp.StatusCode, string(respBody),
		)
	}
	return nil
}

// severityAbove maps a minimum severity to the instruction describing which levels to include.
var severityAbove = map[string]string{
	"critical": "Only include Critical findings.",
	"high":     "Only include High and Critical findings.",
	"medium":   "Only include Medium, High, and Critical findings.",
}

// buildSynthesisPrompt creates the prompt for the synthesis agent.
// When minSeverity is non-empty (and not "low"), a filtering instruction is appended.
func buildSynthesisPrompt(reviews []storage.BatchReviewResult, minSeverity string) string {
	var b strings.Builder
	b.WriteString(`You are combining multiple code review outputs into a single GitHub PR comment.
Rules:
- Deduplicate findings reported by multiple agents
- Organize by severity (Critical > High > Medium > Low)
- Preserve file/line references
- If all agents agree code is clean, say so concisely
- Start with a one-line summary verdict
- Use markdown formatting
- No preamble about yourself
`)

	if instruction, ok := severityAbove[minSeverity]; ok {
		b.WriteString("- Omit findings below " + minSeverity + " severity. " + instruction + "\n")
	}

	b.WriteString("\n")

	// Truncate per-review output to avoid blowing the synthesis agent's context window.
	const maxPerReview = 15000

	for i, r := range reviews {
		fmt.Fprintf(&b, "---\n### Review %d: Agent=%s, Type=%s", i+1, r.Agent, r.ReviewType)
		if r.Status == "failed" {
			b.WriteString(" [FAILED]")
		}
		b.WriteString("\n")
		if r.Output != "" {
			output := r.Output
			if len(output) > maxPerReview {
				output = output[:maxPerReview] + "\n\n...(truncated)"
			}
			b.WriteString(output)
		} else if r.Status == "failed" {
			b.WriteString("(no output — review failed)")
		}
		b.WriteString("\n\n")
	}

	return b.String()
}

// formatSynthesizedComment wraps synthesized output with header and metadata.
func formatSynthesizedComment(output string, reviews []storage.BatchReviewResult, headSHA string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## roborev: Combined Review (`%s`)\n\n", shortSHA(headSHA))
	b.WriteString(output)

	// Build metadata
	agentSet := make(map[string]struct{})
	typeSet := make(map[string]struct{})
	for _, r := range reviews {
		if r.Agent != "" {
			agentSet[r.Agent] = struct{}{}
		}
		if r.ReviewType != "" {
			typeSet[r.ReviewType] = struct{}{}
		}
	}
	var agents, types []string
	for a := range agentSet {
		agents = append(agents, a)
	}
	for t := range typeSet {
		types = append(types, t)
	}

	fmt.Fprintf(&b, "\n\n---\n*Synthesized from %d reviews (agents: %s | types: %s)*\n",
		len(reviews), strings.Join(agents, ", "), strings.Join(types, ", "))

	return b.String()
}

// formatRawBatchComment formats all review outputs as separate details blocks.
// Used as a fallback when synthesis fails.
func formatRawBatchComment(reviews []storage.BatchReviewResult, headSHA string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## roborev: Combined Review (`%s`)\n\n", shortSHA(headSHA))
	b.WriteString("> Synthesis unavailable. Showing raw review outputs.\n\n")

	for _, r := range reviews {
		summary := fmt.Sprintf("Agent: %s | Type: %s | Status: %s", r.Agent, r.ReviewType, r.Status)
		fmt.Fprintf(&b, "<details>\n<summary>%s</summary>\n\n", summary)
		if r.Status == "failed" {
			b.WriteString("**Error:** Review failed. Check daemon logs for details.\n")
		} else if r.Output != "" {
			output := r.Output
			const maxLen = 15000
			if len(output) > maxLen {
				output = output[:maxLen] + "\n\n...(truncated)"
			}
			b.WriteString(output)
		} else {
			b.WriteString("(no output)")
		}
		b.WriteString("\n\n</details>\n\n")
	}

	return b.String()
}

// formatAllFailedComment formats a comment when every job in a batch failed.
func formatAllFailedComment(reviews []storage.BatchReviewResult, headSHA string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## roborev: Review Failed (`%s`)\n\n", shortSHA(headSHA))
	b.WriteString("All review jobs in this batch failed.\n\n")

	for _, r := range reviews {
		fmt.Fprintf(&b, "- **%s** (%s): failed\n", r.Agent, r.ReviewType)
	}

	b.WriteString("\nCheck daemon logs for error details.")

	return b.String()
}

// shortSHA returns the first 8 characters of a SHA, or the full string if shorter.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// formatPRComment formats a review result as a GitHub PR comment in markdown.
func formatPRComment(review *storage.Review, verdict string) string {
	var b strings.Builder

	// Header with verdict
	switch verdict {
	case "P":
		b.WriteString("## roborev: Pass\n\n")
		b.WriteString("No issues found.\n")
	case "F":
		b.WriteString("## roborev: Fail\n\n")
	default:
		b.WriteString("## roborev: Review Complete\n\n")
	}

	// Include review output (truncated if very long)
	output := review.Output
	const maxLen = 60000 // GitHub comment limit is ~65536
	if len(output) > maxLen {
		output = output[:maxLen] + "\n\n...(truncated)"
	}

	if verdict != "P" && output != "" {
		b.WriteString("<details>\n<summary>Review findings</summary>\n\n")
		b.WriteString(output)
		b.WriteString("\n\n</details>\n")
	}

	if review.Job != nil {
		fmt.Fprintf(&b, "\n---\n*Review type: %s | Agent: %s | Job: %d*\n",
			review.Job.ReviewType, review.Job.Agent, review.Job.ID)
	}

	return b.String()
}

// postPRComment posts a comment on a GitHub PR using the gh CLI.
// Truncates the body to stay within GitHub's ~65536 character limit.
func (p *CIPoller) postPRComment(ghRepo string, prNumber int, body string) error {
	const maxCommentLen = 60000 // leave headroom below GitHub's ~65536 limit
	if len(body) > maxCommentLen {
		body = body[:maxCommentLen] + "\n\n...(truncated — comment exceeded size limit)"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "comment",
		"--repo", ghRepo,
		fmt.Sprintf("%d", prNumber),
		"--body-file", "-",
	)
	cmd.Stdin = strings.NewReader(body)
	if env := p.ghEnvForRepo(ghRepo); env != nil {
		cmd.Env = env
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh pr comment: %s: %s", err, string(out))
	}
	return nil
}
