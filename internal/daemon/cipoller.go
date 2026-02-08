package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

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

// CIPoller polls GitHub for open PRs and enqueues security reviews
type CIPoller struct {
	db          *storage.DB
	cfgGetter   ConfigGetter
	broadcaster Broadcaster

	stopCh  chan struct{}
	doneCh  chan struct{}
	mu      sync.Mutex
	running bool
}

// NewCIPoller creates a new CI poller
func NewCIPoller(db *storage.DB, cfgGetter ConfigGetter, broadcaster Broadcaster) *CIPoller {
	return &CIPoller{
		db:          db,
		cfgGetter:   cfgGetter,
		broadcaster: broadcaster,
	}
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

	p.stopCh = make(chan struct{})
	p.doneCh = make(chan struct{})
	p.running = true

	stopCh := p.stopCh
	doneCh := p.doneCh

	go p.run(stopCh, doneCh, interval)

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
	p.running = false
	p.mu.Unlock()

	close(stopCh)
	<-doneCh
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

func (p *CIPoller) run(stopCh, doneCh chan struct{}, interval time.Duration) {
	defer close(doneCh)

	// Poll immediately on start
	p.poll()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			log.Println("CI poller stopped")
			return
		case <-ticker.C:
			p.poll()
		}
	}
}

func (p *CIPoller) poll() {
	cfg := p.cfgGetter.Config()
	for _, ghRepo := range cfg.CI.Repos {
		if err := p.pollRepo(ghRepo, cfg); err != nil {
			log.Printf("CI poller: error polling %s: %v", ghRepo, err)
		}
	}
}

func (p *CIPoller) pollRepo(ghRepo string, cfg *config.Config) error {
	// List open PRs via gh CLI
	prs, err := listOpenPRs(ghRepo)
	if err != nil {
		return fmt.Errorf("list PRs: %w", err)
	}

	for _, pr := range prs {
		if err := p.processPR(ghRepo, pr, cfg); err != nil {
			log.Printf("CI poller: error processing %s#%d: %v", ghRepo, pr.Number, err)
		}
	}
	return nil
}

func (p *CIPoller) processPR(ghRepo string, pr ghPR, cfg *config.Config) error {
	// Check if already reviewed at this HEAD SHA
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
	if err := gitFetch(repo.RootPath); err != nil {
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

	// Determine review type
	reviewType := cfg.CI.ReviewType
	if reviewType == "" {
		reviewType = "security"
	}

	// Enqueue range review
	job, err := p.db.EnqueueJob(storage.EnqueueOpts{
		RepoID:     repo.ID,
		GitRef:     gitRef,
		Agent:      cfg.CI.Agent,
		Model:      cfg.CI.Model,
		Reasoning:  "thorough",
		ReviewType: reviewType,
	})
	if err != nil {
		return fmt.Errorf("enqueue job: %w", err)
	}

	// Record the CI review
	if err := p.db.RecordCIReview(ghRepo, pr.Number, pr.HeadRefOid, job.ID); err != nil {
		return fmt.Errorf("record CI review: %w", err)
	}

	log.Printf("CI poller: enqueued job %d for %s#%d (HEAD=%s, range=%s)",
		job.ID, ghRepo, pr.Number, pr.HeadRefOid[:8], gitRef)

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
		if err == nil {
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

// listOpenPRs uses the gh CLI to list open PRs for a GitHub repo
func listOpenPRs(ghRepo string) ([]ghPR, error) {
	cmd := exec.Command("gh", "pr", "list",
		"--repo", ghRepo,
		"--json", "number,headRefOid,baseRefName,headRefName,title",
		"--state", "open",
		"--limit", "100",
	)
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

// gitFetch runs git fetch in the repo to get latest refs
func gitFetch(repoPath string) error {
	cmd := exec.Command("git", "-C", repoPath, "fetch", "--quiet")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", err, string(out))
	}
	return nil
}
