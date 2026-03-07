package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
)

func TestRepoResolver_Matching(t *testing.T) {
	tests := []struct {
		name          string
		repos         []string
		exclude       []string
		maxRepos      int
		mockAPI       func(ctx context.Context, owner string, repos []string) ([]string, error)
		expectedCalls int
		expected      []string
		expectErr     bool
	}{
		{
			name:  "ExactOnly",
			repos: []string{"acme/api", "acme/web"},
			mockAPI: func(_ context.Context, _ string, _ []string) ([]string, error) {
				return nil, fmt.Errorf("should not be called")
			},
			expectedCalls: 0,
			expected:      []string{"acme/api", "acme/web"},
		},
		{
			name:  "WildcardExpansion",
			repos: []string{"acme/api-*"},
			mockAPI: func(_ context.Context, owner string, _ []string) ([]string, error) {
				if owner == "acme" {
					return []string{"acme/api", "acme/web", "acme/docs", "acme/api-gateway"}, nil
				}
				return nil, fmt.Errorf("unknown owner: %s", owner)
			},
			expectedCalls: 1,
			expected:      []string{"acme/api-gateway"},
		},
		{
			name:  "WildcardStar",
			repos: []string{"myorg/*"},
			mockAPI: func(_ context.Context, owner string, _ []string) ([]string, error) {
				if owner == "myorg" {
					return []string{"myorg/api", "myorg/web", "myorg/docs"}, nil
				}
				return nil, fmt.Errorf("unknown owner: %s", owner)
			},
			expectedCalls: 1,
			expected:      []string{"myorg/api", "myorg/docs", "myorg/web"},
		},
		{
			name:    "ExclusionPatterns",
			repos:   []string{"acme/*"},
			exclude: []string{"acme/internal-*", "acme/archived-*"},
			mockAPI: func(_ context.Context, _ string, _ []string) ([]string, error) {
				return []string{"acme/api", "acme/web", "acme/internal-tools", "acme/internal-docs", "acme/archived-v1"}, nil
			},
			expectedCalls: 1,
			expected:      []string{"acme/api", "acme/web"},
		},
		{
			name:    "ExclusionAppliesToExact",
			repos:   []string{"acme/api", "acme/internal-tools"},
			exclude: []string{"acme/internal-*"},
			mockAPI: func(_ context.Context, _ string, _ []string) ([]string, error) {
				return nil, fmt.Errorf("should not be called")
			},
			expectedCalls: 0,
			expected:      []string{"acme/api"},
		},
		{
			name:     "MaxRepos",
			repos:    []string{"acme/*"},
			maxRepos: 5,
			mockAPI: func(_ context.Context, _ string, _ []string) ([]string, error) {
				repos := make([]string, 200)
				for i := range repos {
					repos[i] = fmt.Sprintf("acme/repo-%03d", i)
				}
				return repos, nil
			},
			expectedCalls: 1,
			expected:      []string{"acme/repo-000", "acme/repo-001", "acme/repo-002", "acme/repo-003", "acme/repo-004"},
		},
		{
			name:  "Deduplication",
			repos: []string{"acme/api", "acme/*"},
			mockAPI: func(_ context.Context, _ string, _ []string) ([]string, error) {
				return []string{"acme/api", "acme/web"}, nil
			},
			expectedCalls: 1,
			expected:      []string{"acme/api", "acme/web"},
		},
		{
			name:  "CaseInsensitiveWildcard",
			repos: []string{"acme/*"},
			mockAPI: func(_ context.Context, _ string, _ []string) ([]string, error) {
				return []string{"Acme/API", "Acme/Web", "Acme/Docs"}, nil
			},
			expectedCalls: 1,
			expected:      []string{"Acme/API", "Acme/Docs", "Acme/Web"},
		},
		{
			name:    "CaseInsensitiveExclusion",
			repos:   []string{"acme/*"},
			exclude: []string{"acme/internal-*"},
			mockAPI: func(_ context.Context, _ string, _ []string) ([]string, error) {
				return []string{"Acme/API", "Acme/Internal-Tools", "Acme/Web"}, nil
			},
			expectedCalls: 1,
			expected:      []string{"Acme/API", "Acme/Web"},
		},
		{
			name:  "CaseInsensitiveDedup",
			repos: []string{"acme/api", "acme/*"},
			mockAPI: func(_ context.Context, _ string, _ []string) ([]string, error) {
				return []string{"Acme/Api", "Acme/Web"}, nil
			},
			expectedCalls: 1,
			expected:      []string{"Acme/Web", "acme/api"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			mockFn := func(ctx context.Context, owner string, repos []string) ([]string, error) {
				calls++
				if tt.mockAPI != nil {
					return tt.mockAPI(ctx, owner, repos)
				}
				return nil, nil
			}
			r := &RepoResolver{listReposFn: mockFn}
			ci := &config.CIConfig{
				Repos:        tt.repos,
				ExcludeRepos: tt.exclude,
				MaxRepos:     tt.maxRepos,
			}

			got, err := r.Resolve(context.Background(), ci, nil)
			if (err != nil) != tt.expectErr {
				t.Fatalf("expected error: %v, got: %v", tt.expectErr, err)
			}
			if !slices.Equal(got, tt.expected) {
				t.Errorf("got %v, want %v", got, tt.expected)
			}
			if calls != tt.expectedCalls {
				t.Errorf("expected %d API calls, got %d", tt.expectedCalls, calls)
			}
		})
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
		t.Fatalf("Resolve: %v", err)
	}
	if len(repos) != 1 || repos[0] != "acme/explicit-repo" {
		t.Errorf("expected [acme/explicit-repo] on API failure, got %v", repos)
	}
	if calls != 1 {
		t.Fatalf("expected 1 API call, got %d", calls)
	}

	// Degraded results must NOT be cached — next call should retry the API
	repos2, err := r.Resolve(ctx, ci, nil)
	if err != nil {
		t.Fatalf("Resolve 2: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected degraded result to NOT be cached (2 calls), got %d", calls)
	}
	if len(repos2) != 1 || repos2[0] != "acme/explicit-repo" {
		t.Errorf("expected [acme/explicit-repo] on second call, got %v", repos2)
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
		t.Fatalf("Resolve 1: %v", err)
	}
	if len(repos1) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos1))
	}

	// Force TTL expiry so next call re-expands
	r.mu.Lock()
	r.cachedAt = time.Now().Add(-2 * time.Hour)
	r.mu.Unlock()

	// Second call fails API but should return stale cache
	repos2, err := r.Resolve(ctx, ci, nil)
	if err != nil {
		t.Fatalf("Resolve 2: %v", err)
	}
	if len(repos2) != 2 {
		t.Errorf("expected stale cache (2 repos) on degraded, got %d: %v", len(repos2), repos2)
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
		t.Fatalf("expected context.Canceled, got: %v", err)
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

	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-ctx.Done()

	_, err := r.Resolve(ctx, ci, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got: %v", err)
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
			if !slices.Equal(got, tt.expect) {
				t.Fatalf("got %v, want %v", got, tt.expect)
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
		t.Fatalf("Resolve: %v", err)
	}
	if len(repos) != 5 {
		t.Fatalf("expected 5 repos (max_repos cap), got %d: %v", len(repos), repos)
	}

	// The explicit repo must always be included regardless of alphabetical position
	if !slices.Contains(repos, "acme/zzz-important") {
		t.Errorf("explicit repo acme/zzz-important was dropped by max_repos truncation: %v", repos)
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
			if !slices.Equal(got, tt.expect) {
				t.Fatalf("got %v, want %v", got, tt.expect)
			}
		})
	}
}
