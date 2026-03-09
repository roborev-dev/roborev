package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRepoResolver_ExactOnly(t *testing.T) {
	var calls int
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			calls++
			return nil, fmt.Errorf("should not be called")
		},
	}

	ci := &config.CIConfig{
		Repos: []string{"acme/api", "acme/web"},
	}

	repos, err := r.Resolve(context.Background(), ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve: %v", err)
	}
	if calls != 0 {
		assert.Condition(t, func() bool {
			return false
		}, "expected 0 API calls for exact-only config, got %d", calls)
	}
	if len(repos) != 2 {
		require.Condition(t, func() bool {
			return false
		}, "expected 2 repos, got %d: %v", len(repos), repos)
	}
}

func TestRepoResolver_WildcardExpansion(t *testing.T) {
	r := &RepoResolver{
		listReposFn: func(_ context.Context, owner string, _ []string) ([]string, error) {
			if owner == "acme" {
				return []string{"acme/api", "acme/web", "acme/docs", "acme/api-gateway"}, nil
			}
			return nil, fmt.Errorf("unknown owner: %s", owner)
		},
	}

	ci := &config.CIConfig{
		Repos: []string{"acme/api-*"},
	}

	repos, err := r.Resolve(context.Background(), ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false

			// path.Match("acme/api-*", "acme/api") is false because "api" doesn't match "api-*"
			// Only acme/api-gateway matches
		}, "Resolve: %v", err)
	}

	if len(repos) != 1 {
		require.Condition(t, func() bool {
			return false
		}, "expected 1 repo matching api-*, got %d: %v", len(repos), repos)
	}
	if repos[0] != "acme/api-gateway" {
		assert.Condition(t, func() bool {
			return false
		}, "expected acme/api-gateway, got %s", repos[0])
	}
}

func TestRepoResolver_WildcardStar(t *testing.T) {
	r := &RepoResolver{
		listReposFn: func(_ context.Context, owner string, _ []string) ([]string, error) {
			if owner == "myorg" {
				return []string{"myorg/api", "myorg/web", "myorg/docs"}, nil
			}
			return nil, fmt.Errorf("unknown owner: %s", owner)
		},
	}

	ci := &config.CIConfig{
		Repos: []string{"myorg/*"},
	}

	repos, err := r.Resolve(context.Background(), ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve: %v", err)
	}
	if len(repos) != 3 {
		require.Condition(t, func() bool {
			return false
		}, "expected 3 repos for myorg/*, got %d: %v", len(repos), repos)
	}
}

func TestRepoResolver_ExclusionPatterns(t *testing.T) {
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			return []string{"acme/api", "acme/web", "acme/internal-tools", "acme/internal-docs", "acme/archived-v1"}, nil
		},
	}

	ci := &config.CIConfig{
		Repos:        []string{"acme/*"},
		ExcludeRepos: []string{"acme/internal-*", "acme/archived-*"},
	}

	repos, err := r.Resolve(context.Background(), ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve: %v", err)
	}
	if len(repos) != 2 {
		require.Condition(t, func() bool {
			return false
		}, "expected 2 repos after exclusions, got %d: %v", len(repos), repos)
	}
	found := make(map[string]bool)
	for _, r := range repos {
		found[r] = true
	}
	if !found["acme/api"] || !found["acme/web"] {
		assert.Condition(t, func() bool {
			return false
		}, "expected acme/api and acme/web, got %v", repos)
	}
}

func TestRepoResolver_ExclusionAppliesToExact(t *testing.T) {
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			return nil, fmt.Errorf("should not be called")
		},
	}

	ci := &config.CIConfig{
		Repos:        []string{"acme/api", "acme/internal-tools"},
		ExcludeRepos: []string{"acme/internal-*"},
	}

	repos, err := r.Resolve(context.Background(), ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve: %v", err)
	}
	if len(repos) != 1 || repos[0] != "acme/api" {
		assert.Condition(t, func() bool {
			return false
		}, "expected [acme/api], got %v", repos)
	}
}

func TestRepoResolver_MaxRepos(t *testing.T) {
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			repos := make([]string, 200)
			for i := range repos {
				repos[i] = fmt.Sprintf("acme/repo-%03d", i)
			}
			return repos, nil
		},
	}

	ci := &config.CIConfig{
		Repos:    []string{"acme/*"},
		MaxRepos: 5,
	}

	repos, err := r.Resolve(context.Background(), ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve: %v", err)
	}
	if len(repos) != 5 {
		require.Condition(t, func() bool {
			return false
		}, "expected 5 repos (max_repos cap), got %d", len(repos))
	}
}

func TestRepoResolver_CacheHit(t *testing.T) {
	var calls int
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			calls++
			return []string{"acme/api", "acme/web"}, nil
		},
	}

	ci := &config.CIConfig{
		Repos: []string{"acme/*"},
	}

	ctx := context.Background()

	// First call populates cache
	repos1, err := r.Resolve(ctx, ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve 1: %v", err)
	}
	if calls != 1 {
		require.Condition(t, func() bool {
			return false
		}, "expected 1 API call, got %d", calls)
	}

	// Second call should hit cache
	repos2, err := r.Resolve(ctx, ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve 2: %v", err)
	}
	if calls != 1 {
		assert.Condition(t, func() bool {
			return false
		}, "expected cache hit (still 1 call), got %d calls", calls)
	}
	if len(repos1) != len(repos2) {
		assert.Condition(t, func() bool {
			return false
		}, "cache returned different length: %d vs %d", len(repos1), len(repos2))
	}
}

func TestRepoResolver_CacheInvalidationOnConfigChange(t *testing.T) {
	var calls int
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			calls++
			return []string{"acme/api"}, nil
		},
	}

	ci1 := &config.CIConfig{
		Repos: []string{"acme/*"},
	}

	ctx := context.Background()
	if _, err := r.Resolve(ctx, ci1, nil); err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve 1: %v", err)
	}
	if calls != 1 {
		require.Condition(t, func() bool {
			return false
		}, "expected 1 call, got %d", calls)
	}

	// Change config — should invalidate cache
	ci2 := &config.CIConfig{
		Repos: []string{"acme/*", "other/repo"},
	}
	if _, err := r.Resolve(ctx, ci2, nil); err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve 2: %v", err)
	}
	if calls != 2 {
		assert.Condition(t, func() bool {
			return false
		}, "expected cache miss on config change (2 calls), got %d", calls)
	}
}

func TestRepoResolver_CacheInvalidationOnTTLExpiry(t *testing.T) {
	var calls int
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			calls++
			return []string{"acme/api"}, nil
		},
	}

	ci := &config.CIConfig{
		Repos: []string{"acme/*"},
	}

	ctx := context.Background()
	if _, err := r.Resolve(ctx, ci, nil); err != nil {
		require.Condition(t, func() bool {
			return false

			// Second call within TTL (1h) should hit cache
		}, "Resolve 1: %v", err)
	}

	if _, err := r.Resolve(ctx, ci, nil); err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve 2: %v", err)
	}
	if calls != 1 {
		assert.Condition(t, func() bool {
			return false
		}, "expected cache hit within TTL floor (1 call), got %d", calls)
	}

	// Force cache miss by backdating cachedAt past the TTL
	r.mu.Lock()
	r.cachedAt = time.Now().Add(-2 * time.Hour)
	r.mu.Unlock()

	if _, err := r.Resolve(ctx, ci, nil); err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve 3: %v", err)
	}
	if calls != 2 {
		assert.Condition(t, func() bool {
			return false
		}, "expected cache miss after TTL expiry (2 calls), got %d", calls)
	}
}

func TestRepoResolver_CacheInvalidationOnMaxReposChange(t *testing.T) {
	var calls int
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			calls++
			repos := make([]string, 20)
			for i := range repos {
				repos[i] = fmt.Sprintf("acme/repo-%02d", i)
			}
			return repos, nil
		},
	}

	ci1 := &config.CIConfig{
		Repos:    []string{"acme/*"},
		MaxRepos: 5,
	}

	ctx := context.Background()
	repos1, err := r.Resolve(ctx, ci1, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve 1: %v", err)
	}
	if len(repos1) != 5 {
		require.Condition(t, func() bool {
			return false
		}, "expected 5 repos, got %d", len(repos1))
	}
	if calls != 1 {
		require.Condition(t, func() bool {
			return false
		}, "expected 1 call, got %d", calls)
	}

	// Change max_repos — should invalidate cache
	ci2 := &config.CIConfig{
		Repos:    []string{"acme/*"},
		MaxRepos: 10,
	}
	repos2, err := r.Resolve(ctx, ci2, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve 2: %v", err)
	}
	if calls != 2 {
		assert.Condition(t, func() bool {
			return false
		}, "expected cache miss on max_repos change (2 calls), got %d", calls)
	}
	if len(repos2) != 10 {
		assert.Condition(t, func() bool {
			return false
		}, "expected 10 repos after max_repos increase, got %d", len(repos2))
	}
}

func TestRepoResolver_APIFailureFallback(t *testing.T) {
	var calls int
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			calls++
			return nil, fmt.Errorf("network error")
		},
	}

	ci := &config.CIConfig{
		Repos: []string{"acme/*", "acme/explicit-repo"},
	}

	ctx := context.Background()

	// Should succeed — wildcards fail but exact entries pass through
	repos, err := r.Resolve(ctx, ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve: %v", err)
	}
	if len(repos) != 1 || repos[0] != "acme/explicit-repo" {
		assert.Condition(t, func() bool {
			return false
		}, "expected [acme/explicit-repo] on API failure, got %v", repos)
	}
	if calls != 1 {
		require.Condition(t, func() bool {
			return false
		}, "expected 1 API call, got %d", calls)
	}

	// Degraded results must NOT be cached — next call should retry the API
	repos2, err := r.Resolve(ctx, ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve 2: %v", err)
	}
	if calls != 2 {
		assert.Condition(t, func() bool {
			return false
		}, "expected degraded result to NOT be cached (2 calls), got %d", calls)
	}
	if len(repos2) != 1 || repos2[0] != "acme/explicit-repo" {
		assert.Condition(t, func() bool {
			return false
		}, "expected [acme/explicit-repo] on second call, got %v", repos2)
	}
}

func TestRepoResolver_EmptyResultsCached(t *testing.T) {
	var calls int
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			calls++
			return []string{}, nil // no repos match
		},
	}

	ci := &config.CIConfig{
		Repos:        []string{"acme/nonexistent-*"},
		ExcludeRepos: []string{},
	}

	ctx := context.Background()

	repos1, err := r.Resolve(ctx, ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve 1: %v", err)
	}
	if len(repos1) != 0 {
		require.Condition(t, func() bool {
			return false
		}, "expected 0 repos, got %d", len(repos1))
	}
	if calls != 1 {
		require.Condition(t, func() bool {
			return false
		}, "expected 1 API call, got %d", calls)
	}

	// Second call should hit cache even though result is empty
	repos2, err := r.Resolve(ctx, ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve 2: %v", err)
	}
	if len(repos2) != 0 {
		require.Condition(t, func() bool {
			return false
		}, "expected 0 repos from cache, got %d", len(repos2))
	}
	if calls != 1 {
		assert.Condition(t, func() bool {
			return false
		}, "expected cache hit for empty result (still 1 call), got %d", calls)
	}
}

func TestRepoResolver_Deduplication(t *testing.T) {
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			return []string{"acme/api", "acme/web"}, nil
		},
	}

	ci := &config.CIConfig{
		// "acme/api" appears as both exact and would match wildcard
		Repos: []string{"acme/api", "acme/*"},
	}

	repos, err := r.Resolve(context.Background(), ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false

			// acme/api should appear only once
		}, "Resolve: %v", err)
	}

	count := 0
	for _, r := range repos {
		if r == "acme/api" {
			count++
		}
	}
	if count != 1 {
		assert.Condition(t, func() bool {
			return false
		}, "expected acme/api to appear once, got %d times in %v", count, repos)
	}
}

func TestRepoResolver_CaseInsensitiveWildcard(t *testing.T) {
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			// GitHub API returns canonical casing
			return []string{"Acme/API", "Acme/Web", "Acme/Docs"}, nil
		},
	}

	ci := &config.CIConfig{
		Repos: []string{"acme/*"}, // lowercase config
	}

	repos, err := r.Resolve(context.Background(), ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve: %v", err)
	}
	if len(repos) != 3 {
		require.Condition(t, func() bool {
			return false
		}, "expected 3 repos (case-insensitive match), got %d: %v", len(repos), repos)
	}
}

func TestRepoResolver_CaseInsensitiveExclusion(t *testing.T) {
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			return []string{"Acme/API", "Acme/Internal-Tools", "Acme/Web"}, nil
		},
	}

	ci := &config.CIConfig{
		Repos:        []string{"acme/*"},
		ExcludeRepos: []string{"acme/internal-*"}, // lowercase excludes canonical case
	}

	repos, err := r.Resolve(context.Background(), ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve: %v", err)
	}
	if len(repos) != 2 {
		require.Condition(t, func() bool {
			return false
		}, "expected 2 repos after case-insensitive exclusion, got %d: %v", len(repos), repos)
	}
	for _, r := range repos {
		if strings.EqualFold(r, "Acme/Internal-Tools") {
			assert.Condition(t, func() bool {
				return false
			}, "excluded repo should not appear: %v", repos)
		}
	}
}

func TestRepoResolver_CaseInsensitiveDedup(t *testing.T) {
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			return []string{"Acme/Api", "Acme/Web"}, nil
		},
	}

	ci := &config.CIConfig{
		// Explicit entry with different case should dedup with wildcard match
		Repos: []string{"acme/api", "acme/*"},
	}

	repos, err := r.Resolve(context.Background(), ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve: %v", err)
	}
	count := 0
	for _, r := range repos {
		if strings.EqualFold(r, "acme/api") {
			count++
		}
	}
	if count != 1 {
		assert.Condition(t, func() bool {
			return false
		}, "expected api to appear once (case-insensitive dedup), got %d in %v", count, repos)
	}
}

func TestRepoResolver_DegradedFallsBackToStaleCache(t *testing.T) {
	callCount := 0
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			callCount++
			if callCount == 1 {
				return []string{"acme/api", "acme/web"}, nil
			}
			return nil, fmt.Errorf("transient API error")
		},
	}

	ci := &config.CIConfig{
		Repos: []string{"acme/*"},
	}
	ctx := context.Background()

	// First call succeeds and caches
	repos1, err := r.Resolve(ctx, ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve 1: %v", err)
	}
	if len(repos1) != 2 {
		require.Condition(t, func() bool {
			return false
		}, "expected 2 repos, got %d", len(repos1))
	}

	// Force TTL expiry so next call re-expands
	r.mu.Lock()
	r.cachedAt = time.Now().Add(-2 * time.Hour)
	r.mu.Unlock()

	// Second call fails API but should return stale cache
	repos2, err := r.Resolve(ctx, ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve 2: %v", err)
	}
	if len(repos2) != 2 {
		assert.Condition(t, func() bool {
			return false
		}, "expected stale cache (2 repos) on degraded, got %d: %v", len(repos2), repos2)
	}
}

func TestRepoResolver_CancelledContextReturnsError(t *testing.T) {
	r := &RepoResolver{
		listReposFn: func(ctx context.Context, _ string, _ []string) ([]string, error) {
			return nil, ctx.Err()
		},
	}

	ci := &config.CIConfig{
		Repos: []string{"acme/*"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := r.Resolve(ctx, ci, nil)
	if !errors.Is(err, context.Canceled) {
		require.Condition(t, func() bool {
			return false
		}, "expected context.Canceled, got: %v", err)
	}
}

func TestRepoResolver_DeadlineExceededReturnsError(t *testing.T) {
	r := &RepoResolver{
		listReposFn: func(ctx context.Context, _ string, _ []string) ([]string, error) {
			return nil, ctx.Err()
		},
	}

	ci := &config.CIConfig{
		Repos: []string{"acme/*"},
	}

	ctx, cancel := context.WithTimeout(
		context.Background(), 0,
	)
	defer cancel()
	// Allow timeout to fire
	time.Sleep(time.Millisecond)

	_, err := r.Resolve(ctx, ci, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		require.Condition(t, func() bool {
			return false
		}, "expected context.DeadlineExceeded, got: %v", err)

	}
}

func TestRepoResolver_EnvFnCalled(t *testing.T) {
	var envOwners []string
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			return []string{"acme/api"}, nil
		},
	}

	ci := &config.CIConfig{
		Repos: []string{"acme/*"},
	}

	envFn := func(owner string) []string {
		envOwners = append(envOwners, owner)
		return []string{"GH_TOKEN=test-token"}
	}

	if _, err := r.Resolve(context.Background(), ci, envFn); err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve: %v", err)
	}
	if len(envOwners) != 1 || envOwners[0] != "acme" {
		assert.Condition(t, func() bool {
			return false
		}, "expected envFn called with [acme], got %v", envOwners)
	}
}

func TestExactReposOnly(t *testing.T) {
	tests := []struct {
		name   string
		repos  []string
		expect []string
	}{
		{"all exact", []string{"acme/api", "acme/web"}, []string{"acme/api", "acme/web"}},
		{"mixed", []string{"acme/api", "acme/*", "other/repo"}, []string{"acme/api", "other/repo"}},
		{"all wildcards", []string{"acme/*", "other/api-*"}, nil},
		{"empty", nil, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExactReposOnly(tt.repos)
			if len(got) != len(tt.expect) {
				require.Condition(t, func() bool {
					return false
				}, "got %v, want %v", got, tt.expect)
			}
			for i := range got {
				if got[i] != tt.expect[i] {
					assert.Condition(t, func() bool {
						return false
					}, "got[%d] = %q, want %q", i, got[i], tt.expect[i])
				}
			}
		})
	}
}

func TestRepoResolver_MaxReposPreservesExplicit(t *testing.T) {
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			// Return many repos that sort before the explicit ones alphabetically
			repos := make([]string, 20)
			for i := range repos {
				repos[i] = fmt.Sprintf("acme/aaa-%02d", i)
			}
			return repos, nil
		},
	}

	ci := &config.CIConfig{
		// "acme/zzz-important" sorts last alphabetically but is explicit
		Repos:    []string{"acme/zzz-important", "acme/*"},
		MaxRepos: 5,
	}

	repos, err := r.Resolve(context.Background(), ci, nil)
	if err != nil {
		require.Condition(t, func() bool {
			return false
		}, "Resolve: %v", err)
	}
	if len(repos) != 5 {
		require.Condition(t, func() bool {
			return false
		}, "expected 5 repos (max_repos cap), got %d: %v", len(repos), repos)
	}

	// The explicit repo must always be included regardless of alphabetical position
	if !slices.Contains(repos, "acme/zzz-important") {
		assert.Condition(t, func() bool {
			return false
		}, "explicit repo acme/zzz-important was dropped by max_repos truncation: %v", repos)
	}
}

func TestApplyExclusions(t *testing.T) {
	tests := []struct {
		name     string
		repos    []string
		patterns []string
		expect   []string
	}{
		{
			"no exclusions",
			[]string{"a/b", "c/d"},
			nil,
			[]string{"a/b", "c/d"},
		},
		{
			"exclude one pattern",
			[]string{"acme/api", "acme/internal-tools", "acme/web"},
			[]string{"acme/internal-*"},
			[]string{"acme/api", "acme/web"},
		},
		{
			"exclude multiple patterns",
			[]string{"acme/api", "acme/archived-v1", "acme/internal-docs"},
			[]string{"acme/archived-*", "acme/internal-*"},
			[]string{"acme/api"},
		},
		{
			"exclude all",
			[]string{"acme/internal-api"},
			[]string{"acme/internal-*"},
			nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy since applyExclusions reuses the backing array
			input := make([]string, len(tt.repos))
			copy(input, tt.repos)
			got := applyExclusions(input, tt.patterns)
			if len(got) != len(tt.expect) {
				require.Condition(t, func() bool {
					return false
				}, "got %v, want %v", got, tt.expect)
			}
			for i := range got {
				if got[i] != tt.expect[i] {
					assert.Condition(t, func() bool {
						return false
					}, "got[%d] = %q, want %q", i, got[i], tt.expect[i])
				}
			}
		})
	}
}
