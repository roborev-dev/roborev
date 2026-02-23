package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
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
		t.Fatalf("Resolve: %v", err)
	}
	if calls != 0 {
		t.Errorf("expected 0 API calls for exact-only config, got %d", calls)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d: %v", len(repos), repos)
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
		t.Fatalf("Resolve: %v", err)
	}
	// path.Match("acme/api-*", "acme/api") is false because "api" doesn't match "api-*"
	// Only acme/api-gateway matches
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo matching api-*, got %d: %v", len(repos), repos)
	}
	if repos[0] != "acme/api-gateway" {
		t.Errorf("expected acme/api-gateway, got %s", repos[0])
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
		t.Fatalf("Resolve: %v", err)
	}
	if len(repos) != 3 {
		t.Fatalf("expected 3 repos for myorg/*, got %d: %v", len(repos), repos)
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
		t.Fatalf("Resolve: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos after exclusions, got %d: %v", len(repos), repos)
	}
	found := make(map[string]bool)
	for _, r := range repos {
		found[r] = true
	}
	if !found["acme/api"] || !found["acme/web"] {
		t.Errorf("expected acme/api and acme/web, got %v", repos)
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
		t.Fatalf("Resolve: %v", err)
	}
	if len(repos) != 1 || repos[0] != "acme/api" {
		t.Errorf("expected [acme/api], got %v", repos)
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
		t.Fatalf("Resolve: %v", err)
	}
	if len(repos) != 5 {
		t.Fatalf("expected 5 repos (max_repos cap), got %d", len(repos))
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
		t.Fatalf("Resolve 1: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 API call, got %d", calls)
	}

	// Second call should hit cache
	repos2, err := r.Resolve(ctx, ci, nil)
	if err != nil {
		t.Fatalf("Resolve 2: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected cache hit (still 1 call), got %d calls", calls)
	}
	if len(repos1) != len(repos2) {
		t.Errorf("cache returned different length: %d vs %d", len(repos1), len(repos2))
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
		t.Fatalf("Resolve 1: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}

	// Change config — should invalidate cache
	ci2 := &config.CIConfig{
		Repos: []string{"acme/*", "other/repo"},
	}
	if _, err := r.Resolve(ctx, ci2, nil); err != nil {
		t.Fatalf("Resolve 2: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected cache miss on config change (2 calls), got %d", calls)
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
		t.Fatalf("Resolve 1: %v", err)
	}

	// Second call within TTL (1h) should hit cache
	if _, err := r.Resolve(ctx, ci, nil); err != nil {
		t.Fatalf("Resolve 2: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected cache hit within TTL floor (1 call), got %d", calls)
	}

	// Force cache miss by backdating cachedAt past the TTL
	r.mu.Lock()
	r.cachedAt = time.Now().Add(-2 * time.Hour)
	r.mu.Unlock()

	if _, err := r.Resolve(ctx, ci, nil); err != nil {
		t.Fatalf("Resolve 3: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected cache miss after TTL expiry (2 calls), got %d", calls)
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
		t.Fatalf("Resolve 1: %v", err)
	}
	if len(repos1) != 5 {
		t.Fatalf("expected 5 repos, got %d", len(repos1))
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}

	// Change max_repos — should invalidate cache
	ci2 := &config.CIConfig{
		Repos:    []string{"acme/*"},
		MaxRepos: 10,
	}
	repos2, err := r.Resolve(ctx, ci2, nil)
	if err != nil {
		t.Fatalf("Resolve 2: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected cache miss on max_repos change (2 calls), got %d", calls)
	}
	if len(repos2) != 10 {
		t.Errorf("expected 10 repos after max_repos increase, got %d", len(repos2))
	}
}

func TestRepoResolver_APIFailureFallback(t *testing.T) {
	r := &RepoResolver{
		listReposFn: func(_ context.Context, _ string, _ []string) ([]string, error) {
			return nil, fmt.Errorf("network error")
		},
	}

	ci := &config.CIConfig{
		Repos: []string{"acme/*", "acme/explicit-repo"},
	}

	// Should succeed — wildcards fail but exact entries pass through
	repos, err := r.Resolve(context.Background(), ci, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(repos) != 1 || repos[0] != "acme/explicit-repo" {
		t.Errorf("expected [acme/explicit-repo] on API failure, got %v", repos)
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
		t.Fatalf("Resolve 1: %v", err)
	}
	if len(repos1) != 0 {
		t.Fatalf("expected 0 repos, got %d", len(repos1))
	}
	if calls != 1 {
		t.Fatalf("expected 1 API call, got %d", calls)
	}

	// Second call should hit cache even though result is empty
	repos2, err := r.Resolve(ctx, ci, nil)
	if err != nil {
		t.Fatalf("Resolve 2: %v", err)
	}
	if len(repos2) != 0 {
		t.Fatalf("expected 0 repos from cache, got %d", len(repos2))
	}
	if calls != 1 {
		t.Errorf("expected cache hit for empty result (still 1 call), got %d", calls)
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
		t.Fatalf("Resolve: %v", err)
	}
	// acme/api should appear only once
	count := 0
	for _, r := range repos {
		if r == "acme/api" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected acme/api to appear once, got %d times in %v", count, repos)
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
		t.Fatalf("Resolve: %v", err)
	}
	if len(envOwners) != 1 || envOwners[0] != "acme" {
		t.Errorf("expected envFn called with [acme], got %v", envOwners)
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
				t.Fatalf("got %v, want %v", got, tt.expect)
			}
			for i := range got {
				if got[i] != tt.expect[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.expect[i])
				}
			}
		})
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
				t.Fatalf("got %v, want %v", got, tt.expect)
			}
			for i := range got {
				if got[i] != tt.expect[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.expect[i])
				}
			}
		})
	}
}
