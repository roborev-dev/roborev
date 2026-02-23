package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
)

// RepoResolver expands wildcard patterns in CI repo config into concrete
// "owner/repo" entries by querying the GitHub API via the gh CLI. Results
// are cached for the configured refresh interval and automatically
// invalidated when the config changes.
type RepoResolver struct {
	mu       sync.Mutex
	cached   []string
	hasCache bool // separate from nil check — empty results are valid cache entries
	cachedAt time.Time
	cacheKey string // derived from pattern+exclusion lists+max_repos for invalidation

	// listReposFn is a test seam. When non-nil it replaces the real
	// gh repo list call. Signature: (ctx, owner, env) → []nameWithOwner.
	listReposFn func(ctx context.Context, owner string, env []string) ([]string, error)
}

// ghEnvFn produces the environment slice for gh CLI calls targeting a
// specific owner. The caller typically passes a closure around
// CIPoller.ghEnvForRepo.
type ghEnvFn func(owner string) []string

// Resolve returns the list of concrete "owner/repo" entries to poll.
// It uses a cached result when the TTL has not expired and the config
// has not changed, otherwise it re-expands wildcard patterns.
func (r *RepoResolver) Resolve(ctx context.Context, ci *config.CIConfig, envFn ghEnvFn) ([]string, error) {
	key := r.buildCacheKey(ci)
	ttl := ci.ResolvedRepoRefreshInterval()

	r.mu.Lock()
	if r.hasCache && r.cacheKey == key && time.Since(r.cachedAt) < ttl {
		result := make([]string, len(r.cached))
		copy(result, r.cached)
		r.mu.Unlock()
		return result, nil
	}
	r.mu.Unlock()

	repos, err := r.expand(ctx, ci, envFn)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.cached = repos
	r.hasCache = true
	r.cachedAt = time.Now()
	r.cacheKey = key
	r.mu.Unlock()

	result := make([]string, len(repos))
	copy(result, repos)
	return result, nil
}

// buildCacheKey creates a string that changes whenever the repo lists,
// exclusion lists, or max_repos change, forcing a cache miss.
func (r *RepoResolver) buildCacheKey(ci *config.CIConfig) string {
	return strings.Join(ci.Repos, "\x00") + "\x01" +
		strings.Join(ci.ExcludeRepos, "\x00") + "\x02" +
		fmt.Sprintf("%d", ci.ResolvedMaxRepos())
}

// expand separates exact entries from wildcard patterns, calls the
// GitHub API once per owner for wildcards, applies matching and
// exclusions, deduplicates, and enforces max_repos.
func (r *RepoResolver) expand(ctx context.Context, ci *config.CIConfig, envFn ghEnvFn) ([]string, error) {
	var exact []string
	// owner → list of full patterns like "owner/pattern"
	wildcardsByOwner := make(map[string][]string)

	for _, entry := range ci.Repos {
		owner, repoPattern, ok := strings.Cut(entry, "/")
		if !ok || owner == "" {
			log.Printf("CI repo resolver: skipping invalid entry %q (expected owner/repo)", entry)
			continue
		}
		if isGlobPattern(repoPattern) {
			wildcardsByOwner[owner] = append(wildcardsByOwner[owner], entry)
		} else {
			exact = append(exact, entry)
		}
	}

	// Expand wildcards by owner
	seen := make(map[string]bool, len(exact))
	var result []string
	for _, e := range exact {
		lower := strings.ToLower(e)
		if !seen[lower] {
			seen[lower] = true
			result = append(result, e)
		}
	}

	for owner, patterns := range wildcardsByOwner {
		var env []string
		if envFn != nil {
			env = envFn(owner)
		}

		repos, err := r.callListRepos(ctx, owner, env)
		if err != nil {
			log.Printf("CI repo resolver: failed to list repos for %q: %v (skipping wildcards for this owner)", owner, err)
			continue
		}

		for _, repo := range repos {
			for _, pattern := range patterns {
				matched, matchErr := path.Match(pattern, repo)
				if matchErr != nil {
					log.Printf("CI repo resolver: invalid pattern %q: %v", pattern, matchErr)
					continue
				}
				if matched {
					lower := strings.ToLower(repo)
					if !seen[lower] {
						seen[lower] = true
						result = append(result, repo)
					}
					break // no need to match other patterns for same repo
				}
			}
		}
	}

	// Apply exclusions
	result = applyExclusions(result, ci.ExcludeRepos)

	// Sort for deterministic output
	sort.Strings(result)

	// Enforce max_repos
	maxRepos := ci.ResolvedMaxRepos()
	if len(result) > maxRepos {
		log.Printf("CI repo resolver: expanded to %d repos, truncating to max_repos=%d", len(result), maxRepos)
		result = result[:maxRepos]
	}

	return result, nil
}

// callListRepos invokes the gh CLI or the test seam to list repos for an owner.
func (r *RepoResolver) callListRepos(ctx context.Context, owner string, env []string) ([]string, error) {
	if r.listReposFn != nil {
		return r.listReposFn(ctx, owner, env)
	}
	return ghListRepos(ctx, owner, env)
}

// ghRepoEntry represents a single entry from `gh repo list --json`.
type ghRepoEntry struct {
	NameWithOwner string `json:"nameWithOwner"`
}

// ghListRepos calls `gh repo list <owner> --json nameWithOwner --no-archived --limit 1000`
// and returns the list of "owner/repo" strings.
func ghListRepos(ctx context.Context, owner string, env []string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "gh", "repo", "list", owner,
		"--json", "nameWithOwner",
		"--no-archived",
		"--limit", "1000",
	)
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh repo list %s: %s", owner, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("gh repo list %s: %w", owner, err)
	}

	var entries []ghRepoEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parse gh repo list output for %s: %w", owner, err)
	}

	repos := make([]string, len(entries))
	for i, e := range entries {
		repos[i] = e.NameWithOwner
	}
	return repos, nil
}

// applyExclusions filters repos matching any of the exclusion patterns.
func applyExclusions(repos []string, patterns []string) []string {
	if len(patterns) == 0 {
		return repos
	}
	filtered := repos[:0]
	for _, repo := range repos {
		excluded := false
		for _, pattern := range patterns {
			matched, err := path.Match(pattern, repo)
			if err != nil {
				log.Printf("CI repo resolver: invalid exclusion pattern %q: %v", pattern, err)
				continue
			}
			if matched {
				excluded = true
				break
			}
		}
		if !excluded {
			filtered = append(filtered, repo)
		}
	}
	return filtered
}

// isGlobPattern returns true if the string contains glob metacharacters.
func isGlobPattern(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// ExactReposOnly returns the subset of ci.Repos that are exact entries
// (no glob characters). Used as a fallback when resolver expansion fails.
func ExactReposOnly(repos []string) []string {
	var exact []string
	for _, entry := range repos {
		owner, repoPattern, ok := strings.Cut(entry, "/")
		if ok && owner != "" && !isGlobPattern(repoPattern) {
			exact = append(exact, entry)
		}
	}
	return exact
}
