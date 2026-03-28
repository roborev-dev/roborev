package github

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	googlegithub "github.com/google/go-github/v84/github"
)

type ClientOption func(*clientOptions) error

type clientOptions struct {
	baseURL    *url.URL
	httpClient *http.Client
}

type Client struct {
	api *googlegithub.Client
}

func ptr[T any](value T) *T {
	p := new(T)
	*p = value
	return p
}

func WithBaseURL(raw string) ClientOption {
	return func(opts *clientOptions) error {
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("parse base URL: %w", err)
		}
		if !strings.HasSuffix(parsed.Path, "/") {
			parsed.Path += "/"
		}
		opts.baseURL = parsed
		return nil
	}
}

func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(opts *clientOptions) error {
		opts.httpClient = httpClient
		return nil
	}
}

func NewClient(token string, opts ...ClientOption) (*Client, error) {
	cfg := clientOptions{}
	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return nil, err
		}
	}

	httpClient := cfg.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	api := googlegithub.NewClient(httpClient)
	if cfg.baseURL != nil {
		api.BaseURL = cfg.baseURL
	}
	if strings.TrimSpace(token) != "" {
		api = api.WithAuthToken(strings.TrimSpace(token))
	}
	return &Client{api: api}, nil
}

func parseRepo(ghRepo string) (string, string, error) {
	owner, repo, ok := strings.Cut(strings.TrimSpace(ghRepo), "/")
	if !ok || owner == "" || repo == "" {
		return "", "", fmt.Errorf("invalid GitHub repo %q", ghRepo)
	}
	return owner, repo, nil
}
