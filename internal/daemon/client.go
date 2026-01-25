package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
)

// Client provides an interface for interacting with the roborev daemon.
// This abstraction allows for easy mocking in tests.
type Client interface {
	// GetReviewBySHA retrieves a review by commit SHA
	GetReviewBySHA(sha string) (*storage.Review, error)

	// GetReviewByJobID retrieves a review by job ID
	GetReviewByJobID(jobID int64) (*storage.Review, error)

	// MarkReviewAddressed marks a review as addressed
	MarkReviewAddressed(reviewID int64) error

	// AddComment adds a comment to a job
	AddComment(jobID int64, commenter, comment string) error

	// EnqueueReview enqueues a review job and returns the job ID
	EnqueueReview(repoPath, gitRef, agentName string) (int64, error)

	// WaitForReview waits for a job to complete and returns the review
	WaitForReview(jobID int64) (*storage.Review, error)

	// FindJobForCommit finds a job for a specific commit in a repo
	FindJobForCommit(repoPath, sha string) (*storage.ReviewJob, error)

	// FindPendingJobForRef finds a queued or running job for any git ref
	FindPendingJobForRef(repoPath, gitRef string) (*storage.ReviewJob, error)

	// GetCommentsForJob fetches comments for a job
	GetCommentsForJob(jobID int64) ([]storage.Response, error)
}

// DefaultPollInterval is the default polling interval for WaitForReview.
// Tests can override this to speed up polling-based tests.
var DefaultPollInterval = 2 * time.Second

// HTTPClient is the default HTTP-based implementation of Client
type HTTPClient struct {
	addr         string
	httpClient   *http.Client
	pollInterval time.Duration
}

// NewHTTPClient creates a new HTTP daemon client
func NewHTTPClient(addr string) *HTTPClient {
	return &HTTPClient{
		addr:         addr,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		pollInterval: DefaultPollInterval,
	}
}

// NewHTTPClientFromRuntime creates an HTTP client using daemon runtime info
func NewHTTPClientFromRuntime() (*HTTPClient, error) {
	var lastErr error
	for i := 0; i < 5; i++ {
		info, err := GetAnyRunningDaemon()
		if err == nil {
			return NewHTTPClient(fmt.Sprintf("http://%s", info.Addr)), nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("daemon not running: %w", lastErr)
}

// SetPollInterval sets the polling interval for WaitForReview
func (c *HTTPClient) SetPollInterval(interval time.Duration) {
	c.pollInterval = interval
}

func (c *HTTPClient) GetReviewBySHA(sha string) (*storage.Review, error) {
	resp, err := c.httpClient.Get(fmt.Sprintf("%s/api/review?sha=%s", c.addr, sha))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %s", resp.Status)
	}

	var review storage.Review
	if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
		return nil, err
	}

	return &review, nil
}

func (c *HTTPClient) GetReviewByJobID(jobID int64) (*storage.Review, error) {
	resp, err := c.httpClient.Get(fmt.Sprintf("%s/api/review?job_id=%d", c.addr, jobID))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %s", resp.Status)
	}

	var review storage.Review
	if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
		return nil, err
	}

	return &review, nil
}

func (c *HTTPClient) MarkReviewAddressed(reviewID int64) error {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"review_id": reviewID,
		"addressed": true,
	})

	resp, err := c.httpClient.Post(c.addr+"/api/review/address", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mark addressed: %s: %s", resp.Status, body)
	}

	return nil
}

func (c *HTTPClient) AddComment(jobID int64, commenter, comment string) error {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"job_id":    jobID,
		"commenter": commenter,
		"comment":   comment,
	})

	resp, err := c.httpClient.Post(c.addr+"/api/comment", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add comment: %s: %s", resp.Status, body)
	}

	return nil
}

func (c *HTTPClient) EnqueueReview(repoPath, gitRef, agentName string) (int64, error) {
	reqBody, _ := json.Marshal(map[string]string{
		"repo_path": repoPath,
		"git_ref":   gitRef,
		"agent":     agentName,
	})

	resp, err := c.httpClient.Post(c.addr+"/api/enqueue", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("enqueue failed: %s", body)
	}

	var job storage.ReviewJob
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return 0, err
	}

	return job.ID, nil
}

func (c *HTTPClient) WaitForReview(jobID int64) (*storage.Review, error) {
	missingReviewAttempts := 0
	for {
		resp, err := c.httpClient.Get(fmt.Sprintf("%s/api/jobs?id=%d", c.addr, jobID))
		if err != nil {
			return nil, fmt.Errorf("polling job %d: %w", jobID, err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("polling job %d: server returned %s", jobID, resp.Status)
		}

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("polling job %d: decode error: %w", jobID, err)
		}
		resp.Body.Close()

		if len(result.Jobs) == 0 {
			return nil, fmt.Errorf("job %d not found", jobID)
		}

		job := result.Jobs[0]
		switch job.Status {
		case storage.JobStatusDone:
			review, err := c.GetReviewByJobID(jobID)
			if err != nil {
				return nil, err
			}
			if review != nil {
				return review, nil
			}
			missingReviewAttempts++
			if missingReviewAttempts > 5 {
				return nil, fmt.Errorf("review for job %d not found", jobID)
			}
		case storage.JobStatusFailed:
			return nil, fmt.Errorf("job %d failed: %s", jobID, job.Error)
		case storage.JobStatusCanceled:
			return nil, fmt.Errorf("job %d was canceled", jobID)
		}

		time.Sleep(c.pollInterval)
	}
}

func (c *HTTPClient) FindJobForCommit(repoPath, sha string) (*storage.ReviewJob, error) {
	// Normalize repo path to main repo root to handle worktrees consistently.
	// The daemon stores jobs using the main repo root, so we need to match that.
	normalizedRepo := repoPath
	if mainRoot, err := git.GetMainRepoRoot(repoPath); err == nil {
		normalizedRepo = mainRoot
	}
	// Also resolve symlinks and make absolute
	if resolved, err := filepath.EvalSymlinks(normalizedRepo); err == nil {
		normalizedRepo = resolved
	}
	if abs, err := filepath.Abs(normalizedRepo); err == nil {
		normalizedRepo = abs
	}

	// Query by git_ref and repo to avoid matching jobs from different repos
	queryURL := fmt.Sprintf("%s/api/jobs?git_ref=%s&repo=%s&limit=1",
		c.addr, url.QueryEscape(sha), url.QueryEscape(normalizedRepo))

	resp, err := c.httpClient.Get(queryURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query for %s: server returned %s", sha, resp.Status)
	}

	var result struct {
		Jobs []storage.ReviewJob `json:"jobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("query for %s: decode error: %w", sha, err)
	}

	if len(result.Jobs) > 0 {
		return &result.Jobs[0], nil
	}

	// Fallback: if repo filter yielded no results, try git_ref only.
	// This handles worktrees where daemon stores the main repo root path
	// but the caller uses the worktree path.
	fallbackURL := fmt.Sprintf("%s/api/jobs?git_ref=%s&limit=100", c.addr, url.QueryEscape(sha))
	fallbackResp, err := c.httpClient.Get(fallbackURL)
	if err != nil {
		return nil, fmt.Errorf("fallback query for %s: %w", sha, err)
	}
	defer fallbackResp.Body.Close()

	if fallbackResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fallback query for %s: server returned %s", sha, fallbackResp.Status)
	}

	var fallbackResult struct {
		Jobs []storage.ReviewJob `json:"jobs"`
	}
	if err := json.NewDecoder(fallbackResp.Body).Decode(&fallbackResult); err != nil {
		return nil, fmt.Errorf("fallback query for %s: decode error: %w", sha, err)
	}

	// Filter client-side: find a job whose repo path matches when normalized
	for i := range fallbackResult.Jobs {
		job := &fallbackResult.Jobs[i]
		jobRepo := job.RepoPath
		// Skip empty or relative paths to avoid false matches
		if jobRepo == "" || !filepath.IsAbs(jobRepo) {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(jobRepo); err == nil {
			jobRepo = resolved
		}
		if jobRepo == normalizedRepo {
			return job, nil
		}
	}

	return nil, nil
}

func (c *HTTPClient) FindPendingJobForRef(repoPath, gitRef string) (*storage.ReviewJob, error) {
	// Normalize repo path to main repo root
	normalizedRepo := repoPath
	if mainRoot, err := git.GetMainRepoRoot(repoPath); err == nil {
		normalizedRepo = mainRoot
	}
	if resolved, err := filepath.EvalSymlinks(normalizedRepo); err == nil {
		normalizedRepo = resolved
	}
	if abs, err := filepath.Abs(normalizedRepo); err == nil {
		normalizedRepo = abs
	}

	// Use server-side status filtering to find pending jobs.
	// Query for queued first, then running - this avoids pagination issues.
	for _, status := range []string{"queued", "running"} {
		queryURL := fmt.Sprintf("%s/api/jobs?git_ref=%s&repo=%s&status=%s&limit=1",
			c.addr, url.QueryEscape(gitRef), url.QueryEscape(normalizedRepo), status)

		resp, err := c.httpClient.Get(queryURL)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("query for %s: server returned %s", gitRef, resp.Status)
		}

		var result struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("query for %s: decode error: %w", gitRef, err)
		}
		resp.Body.Close()

		if len(result.Jobs) > 0 {
			return &result.Jobs[0], nil
		}
	}

	return nil, nil
}

func (c *HTTPClient) GetCommentsForJob(jobID int64) ([]storage.Response, error) {
	resp, err := c.httpClient.Get(fmt.Sprintf("%s/api/comments?job_id=%d", c.addr, jobID))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch responses: %s", resp.Status)
	}

	var result struct {
		Responses []storage.Response `json:"responses"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Responses, nil
}
