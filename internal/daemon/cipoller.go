package daemon

import (
	"context"
	"encoding/json"
	"fmt"
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

	cfg := cfgGetter.Config()
	if cfg.CI.GitHubAppConfigured() {
		pemData, err := cfg.CI.GitHubAppPrivateKeyResolved()
		if err != nil {
			log.Printf("CI poller: failed to load GitHub App private key: %v", err)
		} else {
			tp, err := NewGitHubAppTokenProvider(cfg.CI.GitHubAppID, cfg.CI.GitHubAppInstallationID, pemData)
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
}

func (p *CIPoller) pollRepo(ctx context.Context, ghRepo string, cfg *config.Config) error {
	// List open PRs via gh CLI
	prs, err := p.listOpenPRs(ctx, ghRepo)
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

	// Fetch latest refs
	if err := gitFetchCtx(ctx, repo.RootPath); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}

	// Determine merge base
	baseRef := "origin/" + pr.BaseRefName
	mergeBase, err := gitpkg.GetMergeBase(repo.RootPath, baseRef, pr.HeadRefOid)
	if err != nil {
		return fmt.Errorf("merge-base %s %s: %w", baseRef, pr.HeadRefOid, err)
	}

	// Build git ref for range review
	gitRef := mergeBase + ".." + pr.HeadRefOid

	// Resolve review types and agents from config
	reviewTypes := cfg.CI.ResolvedReviewTypes()
	agents := cfg.CI.ResolvedAgents()
	totalJobs := len(reviewTypes) * len(agents)

	// Create batch
	batch, err := p.db.CreateCIBatch(ghRepo, pr.Number, pr.HeadRefOid, totalJobs)
	if err != nil {
		return fmt.Errorf("create CI batch: %w", err)
	}

	// Enqueue jobs for each review_type x agent combination
	for _, rt := range reviewTypes {
		for _, ag := range agents {
			job, err := p.db.EnqueueJob(storage.EnqueueOpts{
				RepoID:     repo.ID,
				GitRef:     gitRef,
				Agent:      ag,
				Model:      cfg.CI.Model,
				Reasoning:  "thorough",
				ReviewType: rt,
			})
			if err != nil {
				return fmt.Errorf("enqueue job (type=%s, agent=%s): %w", rt, ag, err)
			}

			if err := p.db.RecordBatchJob(batch.ID, job.ID); err != nil {
				return fmt.Errorf("record batch job: %w", err)
			}

			log.Printf("CI poller: enqueued job %d for %s#%d (type=%s, agent=%s, range=%s)",
				job.ID, ghRepo, pr.Number, rt, ag, gitRef)
		}
	}

	log.Printf("CI poller: created batch %d for %s#%d (HEAD=%s, %d jobs)",
		batch.ID, ghRepo, pr.Number, pr.HeadRefOid[:8], totalJobs)

	return nil
}

// findLocalRepo finds the local repo that corresponds to a GitHub "owner/repo" identifier.
// It looks for repos whose identity contains the owner/repo pattern.
func (p *CIPoller) findLocalRepo(ghRepo string) (*storage.Repo, error) {
	// Try common identity patterns:
	// - git@github.com:owner/repo.git
	// - https://github.com/owner/repo.git
	// - https://github.com/owner/repo
	patterns := []string{
		"git@github.com:" + ghRepo + ".git",
		"https://github.com/" + ghRepo + ".git",
		"https://github.com/" + ghRepo,
	}

	for _, pattern := range patterns {
		repo, err := p.db.GetRepoByIdentity(pattern)
		if err == nil && repo != nil {
			return repo, nil
		}
	}

	// Fall back: search all repos and check if identity ends with owner/repo
	return p.findRepoByPartialIdentity(ghRepo)
}

// findRepoByPartialIdentity searches repos for a matching GitHub owner/repo pattern
func (p *CIPoller) findRepoByPartialIdentity(ghRepo string) (*storage.Repo, error) {
	rows, err := p.db.Query(`SELECT id, root_path, name, identity FROM repos WHERE identity IS NOT NULL AND identity != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Normalize the search pattern: owner/repo (without .git)
	needle := strings.TrimSuffix(ghRepo, ".git")

	for rows.Next() {
		var repo storage.Repo
		var identity string
		if err := rows.Scan(&repo.ID, &repo.RootPath, &repo.Name, &identity); err != nil {
			continue
		}
		// Check if identity contains the owner/repo pattern
		// Strip .git suffix for comparison
		normalized := strings.TrimSuffix(identity, ".git")
		if strings.HasSuffix(normalized, "/"+needle) || strings.HasSuffix(normalized, ":"+needle) {
			repo.Identity = identity
			return &repo, nil
		}
	}

	return nil, fmt.Errorf("no local repo found matching %q (run 'roborev init' in a local checkout)", ghRepo)
}

// ghEnv returns the environment for gh CLI commands.
// When GitHub App auth is configured, it injects GH_TOKEN so gh authenticates as the bot.
// Otherwise returns nil (gh uses its default auth).
func (p *CIPoller) ghEnv() []string {
	if p.tokenProvider == nil {
		return nil
	}
	token, err := p.tokenProvider.Token()
	if err != nil {
		log.Printf("CI poller: WARNING: GitHub App token failed, falling back to default gh auth: %v", err)
		return nil
	}
	return append(os.Environ(), "GH_TOKEN="+token)
}

// listOpenPRs uses the gh CLI to list open PRs for a GitHub repo
func (p *CIPoller) listOpenPRs(ctx context.Context, ghRepo string) ([]ghPR, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"--repo", ghRepo,
		"--json", "number,headRefOid,baseRefName,headRefName,title",
		"--state", "open",
		"--limit", "100",
	)
	if env := p.ghEnv(); env != nil {
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
			case "review.failed":
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
	if err := p.postPRComment(ciReview.GithubRepo, ciReview.PRNumber, comment); err != nil {
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

// postBatchResults gathers all review outputs for a batch and posts a combined PR comment.
func (p *CIPoller) postBatchResults(batch *storage.CIPRBatch) {
	reviews, err := p.db.GetBatchReviews(batch.ID)
	if err != nil {
		log.Printf("CI poller: error getting batch reviews for batch %d: %v", batch.ID, err)
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
			return
		}
		verdict := ""
		if review.Job != nil && review.Job.Verdict != nil {
			verdict = *review.Job.Verdict
		}
		comment = formatPRComment(review, verdict)
	} else if successCount == 0 {
		// All jobs failed — post raw error comment
		comment = formatAllFailedComment(reviews)
	} else {
		// Multiple jobs — try synthesis
		cfg := p.cfgGetter.Config()
		synthesized, err := p.synthesizeBatchResults(batch, reviews, cfg)
		if err != nil {
			log.Printf("CI poller: synthesis failed for batch %d: %v (falling back to raw)", batch.ID, err)
			comment = formatRawBatchComment(reviews)
		} else {
			comment = synthesized
		}
	}

	if err := p.postPRComment(batch.GithubRepo, batch.PRNumber, comment); err != nil {
		log.Printf("CI poller: error posting batch comment for %s#%d: %v",
			batch.GithubRepo, batch.PRNumber, err)
		return
	}

	if err := p.db.MarkBatchSynthesized(batch.ID); err != nil {
		log.Printf("CI poller: error marking batch %d synthesized: %v", batch.ID, err)
	}

	log.Printf("CI poller: posted batch comment on %s#%d (batch %d, %d reviews)",
		batch.GithubRepo, batch.PRNumber, batch.ID, len(reviews))
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

	prompt := buildSynthesisPrompt(reviews)

	// Use empty commit SHA since this is synthesis, not a repo review
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	output, err := synthesisAgent.Review(ctx, "", "", prompt, nil)
	if err != nil {
		return "", fmt.Errorf("synthesis review: %w", err)
	}

	return formatSynthesizedComment(output, reviews), nil
}

// buildSynthesisPrompt creates the prompt for the synthesis agent.
func buildSynthesisPrompt(reviews []storage.BatchReviewResult) string {
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

	for i, r := range reviews {
		b.WriteString(fmt.Sprintf("---\n### Review %d: Agent=%s, Type=%s", i+1, r.Agent, r.ReviewType))
		if r.Status == "failed" {
			b.WriteString(fmt.Sprintf(" [FAILED: %s]", r.Error))
		}
		b.WriteString("\n")
		if r.Output != "" {
			b.WriteString(r.Output)
		} else if r.Status == "failed" {
			b.WriteString("(no output — review failed)")
		}
		b.WriteString("\n\n")
	}

	return b.String()
}

// formatSynthesizedComment wraps synthesized output with header and metadata.
func formatSynthesizedComment(output string, reviews []storage.BatchReviewResult) string {
	var b strings.Builder
	b.WriteString("## roborev: Combined Review\n\n")
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

	b.WriteString(fmt.Sprintf("\n\n---\n*Synthesized from %d reviews (agents: %s | types: %s)*\n",
		len(reviews), strings.Join(agents, ", "), strings.Join(types, ", ")))

	return b.String()
}

// formatRawBatchComment formats all review outputs as separate details blocks.
// Used as a fallback when synthesis fails.
func formatRawBatchComment(reviews []storage.BatchReviewResult) string {
	var b strings.Builder
	b.WriteString("## roborev: Combined Review\n\n")
	b.WriteString("> Synthesis unavailable. Showing raw review outputs.\n\n")

	for _, r := range reviews {
		summary := fmt.Sprintf("Agent: %s | Type: %s | Status: %s", r.Agent, r.ReviewType, r.Status)
		b.WriteString(fmt.Sprintf("<details>\n<summary>%s</summary>\n\n", summary))
		if r.Status == "failed" {
			b.WriteString(fmt.Sprintf("**Error:** %s\n", r.Error))
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
func formatAllFailedComment(reviews []storage.BatchReviewResult) string {
	var b strings.Builder
	b.WriteString("## roborev: Review Failed\n\n")
	b.WriteString("All review jobs in this batch failed.\n\n")

	for _, r := range reviews {
		b.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", r.Agent, r.ReviewType, r.Error))
	}

	return b.String()
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
		b.WriteString(fmt.Sprintf("\n---\n*Review type: %s | Agent: %s | Job: %d*\n",
			review.Job.ReviewType, review.Job.Agent, review.Job.ID))
	}

	return b.String()
}

// postPRComment posts a comment on a GitHub PR using the gh CLI.
func (p *CIPoller) postPRComment(ghRepo string, prNumber int, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "comment",
		"--repo", ghRepo,
		fmt.Sprintf("%d", prNumber),
		"--body", body,
	)
	if env := p.ghEnv(); env != nil {
		cmd.Env = env
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh pr comment: %s: %s", err, string(out))
	}
	return nil
}
