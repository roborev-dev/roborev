package github

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	googlegithub "github.com/google/go-github/v84/github"
)

type OpenPullRequest struct {
	Number      int
	HeadRefOID  string
	BaseRefName string
	HeadRefName string
	Title       string
	AuthorLogin string
}

func (c *Client) ListOpenPullRequests(ctx context.Context, ghRepo string, limit int) ([]OpenPullRequest, error) {
	owner, repo, err := parseRepo(ghRepo)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}

	opts := &googlegithub.PullRequestListOptions{
		State: "open",
		ListOptions: googlegithub.ListOptions{
			PerPage: limit,
		},
	}

	prs, _, err := c.api.PullRequests.List(ctx, owner, repo, opts)
	if err != nil {
		return nil, fmt.Errorf("list pull requests: %w", err)
	}

	result := make([]OpenPullRequest, 0, len(prs))
	for _, pr := range prs {
		result = append(result, OpenPullRequest{
			Number:      pr.GetNumber(),
			HeadRefOID:  pr.GetHead().GetSHA(),
			BaseRefName: pr.GetBase().GetRef(),
			HeadRefName: pr.GetHead().GetRef(),
			Title:       pr.GetTitle(),
			AuthorLogin: pr.GetUser().GetLogin(),
		})
	}
	return result, nil
}

func (c *Client) IsPullRequestOpen(ctx context.Context, ghRepo string, prNumber int) (bool, error) {
	owner, repo, err := parseRepo(ghRepo)
	if err != nil {
		return false, err
	}

	pr, _, err := c.api.PullRequests.Get(ctx, owner, repo, prNumber)
	if err != nil {
		return false, fmt.Errorf("get pull request: %w", err)
	}
	return strings.EqualFold(pr.GetState(), "open"), nil
}

func (c *Client) ListOwnerRepos(ctx context.Context, owner string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 1000
	}

	repos, orgErr := c.listOrgRepos(ctx, owner, limit)
	if orgErr == nil {
		return repos, nil
	}
	if !isGitHubStatus(orgErr, 404) {
		return nil, orgErr
	}

	userRepos, userErr := c.listUserRepos(ctx, owner, limit)
	if userErr != nil {
		return nil, userErr
	}
	return userRepos, nil
}

func (c *Client) SetCommitStatus(ctx context.Context, ghRepo, sha, state, description string) error {
	owner, repo, err := parseRepo(ghRepo)
	if err != nil {
		return err
	}

	_, _, err = c.api.Repositories.CreateStatus(ctx, owner, repo, sha, googlegithub.RepoStatus{
		State:       ptr(state),
		Description: ptr(description),
		Context:     ptr("roborev"),
	})
	if err != nil {
		return fmt.Errorf("create commit status: %w", err)
	}
	return nil
}

func CloneURL(ghRepo, token string) (string, error) {
	if _, _, err := parseRepo(ghRepo); err != nil {
		return "", err
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Sprintf("https://github.com/%s.git", ghRepo), nil
	}
	return fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", url.PathEscape(strings.TrimSpace(token)), ghRepo), nil
}

func (c *Client) listOrgRepos(ctx context.Context, owner string, limit int) ([]string, error) {
	opts := &googlegithub.RepositoryListByOrgOptions{
		Type: "all",
		ListOptions: googlegithub.ListOptions{
			PerPage: min(limit, 100),
		},
	}
	return c.collectRepos(ctx, limit, func() ([]*googlegithub.Repository, *googlegithub.Response, error) {
		return c.api.Repositories.ListByOrg(ctx, owner, opts)
	}, func(nextPage int) {
		opts.Page = nextPage
	})
}

func (c *Client) listUserRepos(ctx context.Context, owner string, limit int) ([]string, error) {
	seen := make(map[string]struct{})
	var repos []string

	userOpts := &googlegithub.RepositoryListByUserOptions{
		Type: "owner",
		ListOptions: googlegithub.ListOptions{
			PerPage: min(limit, 100),
		},
	}
	pageRepos, err := c.collectRepos(ctx, limit, func() ([]*googlegithub.Repository, *googlegithub.Response, error) {
		return c.api.Repositories.ListByUser(ctx, owner, userOpts)
	}, func(nextPage int) {
		userOpts.Page = nextPage
	})
	if err != nil {
		return nil, err
	}
	for _, repo := range pageRepos {
		if _, ok := seen[strings.ToLower(repo)]; ok {
			continue
		}
		seen[strings.ToLower(repo)] = struct{}{}
		repos = append(repos, repo)
	}

	authOpts := &googlegithub.RepositoryListByAuthenticatedUserOptions{
		Affiliation: "owner",
		Visibility:  "all",
		ListOptions: googlegithub.ListOptions{
			PerPage: min(limit, 100),
		},
	}
	for {
		authPage, resp, err := c.api.Repositories.ListByAuthenticatedUser(ctx, authOpts)
		if err != nil {
			break
		}
		for _, repo := range authPage {
			fullName := repo.GetFullName()
			if repo.GetArchived() || !strings.EqualFold(strings.TrimSpace(repoOwner(repo)), owner) {
				continue
			}
			if _, ok := seen[strings.ToLower(fullName)]; ok {
				continue
			}
			seen[strings.ToLower(fullName)] = struct{}{}
			repos = append(repos, fullName)
			if len(repos) >= limit {
				slices.Sort(repos)
				return repos[:limit], nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		authOpts.Page = resp.NextPage
	}

	slices.Sort(repos)
	if len(repos) > limit {
		repos = repos[:limit]
	}
	return repos, nil
}

func (c *Client) collectRepos(ctx context.Context, limit int, fetch func() ([]*googlegithub.Repository, *googlegithub.Response, error), setPage func(int)) ([]string, error) {
	var repos []string
	for {
		pageRepos, resp, err := fetch()
		if err != nil {
			return nil, fmt.Errorf("list repositories: %w", err)
		}
		for _, repo := range pageRepos {
			if repo.GetArchived() {
				continue
			}
			repos = append(repos, repo.GetFullName())
			if len(repos) >= limit {
				slices.Sort(repos)
				return repos, nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			slices.Sort(repos)
			return repos, nil
		}
		setPage(resp.NextPage)
	}
}

func repoOwner(repo *googlegithub.Repository) string {
	if repo == nil || repo.Owner == nil {
		return ""
	}
	return repo.Owner.GetLogin()
}
