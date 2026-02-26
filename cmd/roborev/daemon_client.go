package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/roborev-dev/roborev/internal/daemon"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/spf13/cobra"
)

// waitForJob polls until a job completes and displays the review
// Uses the provided serverAddr to ensure we poll the same daemon that received the job.
func waitForJob(cmd *cobra.Command, serverAddr string, jobID int64, quiet bool) error {
	client := &http.Client{Timeout: 5 * time.Second}

	if !quiet {
		cmd.Printf("Waiting for review to complete...")
	}

	// Poll with exponential backoff
	pollInterval := pollStartInterval
	maxInterval := pollMaxInterval
	unknownStatusCount := 0
	const maxUnknownRetries = 10 // Give up after 10 consecutive unknown statuses

	for {
		resp, err := client.Get(fmt.Sprintf("%s/api/jobs?id=%d", serverAddr, jobID))
		if err != nil {
			return fmt.Errorf("failed to check job status: %w", err)
		}

		// Handle non-200 responses
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("server error checking job status (%d): %s", resp.StatusCode, body)
		}

		var jobsResp struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&jobsResp); err != nil {
			resp.Body.Close()
			return fmt.Errorf("failed to parse job status: %w", err)
		}
		resp.Body.Close()

		if len(jobsResp.Jobs) == 0 {
			return fmt.Errorf("%w: %d", ErrJobNotFound, jobID)
		}

		job := jobsResp.Jobs[0]

		switch job.Status {
		case storage.JobStatusDone:
			if !quiet {
				cmd.Printf(" done!\n\n")
			}
			// Fetch and display the review
			return showReview(cmd, serverAddr, jobID, quiet)

		case storage.JobStatusFailed:
			if !quiet {
				cmd.Printf(" failed!\n")
			}
			return fmt.Errorf("review failed: %s", job.Error)

		case storage.JobStatusCanceled:
			if !quiet {
				cmd.Printf(" canceled!\n")
			}
			return fmt.Errorf("review was canceled")

		case storage.JobStatusQueued, storage.JobStatusRunning:
			// Still in progress, continue polling
			unknownStatusCount = 0 // Reset counter on known status
			time.Sleep(pollInterval)
			if pollInterval < maxInterval {
				pollInterval = min(
					// 1.5x backoff
					pollInterval*3/2, maxInterval)
			}

		default:
			// Unknown status - treat as transient for forward-compatibility
			// (daemon may add new statuses in the future)
			unknownStatusCount++
			if unknownStatusCount >= maxUnknownRetries {
				return fmt.Errorf("received unknown status %q %d times, giving up (daemon may be newer than CLI)", job.Status, unknownStatusCount)
			}
			if !quiet {
				cmd.Printf("\n(unknown status %q, continuing to poll...)", job.Status)
			}
			time.Sleep(pollInterval)
			if pollInterval < maxInterval {
				pollInterval = min(pollInterval*3/2, maxInterval)
			}
		}
	}
}

// showReview fetches and displays a review by job ID
// When quiet is true, suppresses output but still returns exit code based on verdict.
func showReview(cmd *cobra.Command, addr string, jobID int64, quiet bool) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("%s/api/review?job_id=%d", addr, jobID))
	if err != nil {
		return fmt.Errorf("failed to fetch review: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("no review found for job %d", jobID)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error fetching review (%d): %s", resp.StatusCode, body)
	}

	var review storage.Review
	if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
		return fmt.Errorf("failed to parse review: %w", err)
	}

	if !quiet {
		cmd.Printf("Review (by %s)\n", review.Agent)
		cmd.Println(strings.Repeat("-", 60))
		cmd.Println(review.Output)
	}

	// Return exit code based on verdict
	verdict := storage.ParseVerdict(review.Output)
	if verdict == "F" {
		// Use a special error that cobra will treat as exit code 1
		return &exitError{code: 1}
	}

	return nil
}

// findJobForCommit finds a job for the given commit SHA in the specified repo
func findJobForCommit(repoPath, sha string) (*storage.ReviewJob, error) {
	addr := getDaemonAddr()
	client := &http.Client{Timeout: 5 * time.Second}

	// Normalize repo path to handle symlinks/relative paths consistently
	normalizedRepo := repoPath
	if resolved, err := filepath.EvalSymlinks(repoPath); err == nil {
		normalizedRepo = resolved
	}
	if abs, err := filepath.Abs(normalizedRepo); err == nil {
		normalizedRepo = abs
	}

	// Query by git_ref and repo to avoid matching jobs from different repos
	queryURL := fmt.Sprintf("%s/api/jobs?git_ref=%s&repo=%s&limit=1",
		addr, url.QueryEscape(sha), url.QueryEscape(normalizedRepo))
	resp, err := client.Get(queryURL)
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

	// Fallback: if repo filter yielded no results, try git_ref only
	// This handles cases where daemon stores paths differently
	// Fetch jobs and filter client-side to avoid cross-repo mismatch
	// Use high limit since we're filtering client-side; in practice same SHA
	// across many repos is rare
	fallbackURL := fmt.Sprintf("%s/api/jobs?git_ref=%s&limit=100", addr, url.QueryEscape(sha))
	fallbackResp, err := client.Get(fallbackURL)
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
		// Skip empty or relative paths to avoid false matches from cwd resolution
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

// waitForReview waits for a review to complete and returns it
func waitForReview(jobID int64) (*storage.Review, error) {
	return waitForReviewWithInterval(jobID, pollStartInterval)
}

func waitForReviewWithInterval(jobID int64, pollInterval time.Duration) (*storage.Review, error) {
	addr := getDaemonAddr()
	client := &http.Client{Timeout: 10 * time.Second}

	for {
		resp, err := client.Get(fmt.Sprintf("%s/api/jobs?id=%d", addr, jobID))
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
			// Get the review
			reviewResp, err := client.Get(fmt.Sprintf("%s/api/review?job_id=%d", addr, jobID))
			if err != nil {
				return nil, err
			}
			defer reviewResp.Body.Close()

			var review storage.Review
			if err := json.NewDecoder(reviewResp.Body).Decode(&review); err != nil {
				return nil, err
			}
			return &review, nil

		case storage.JobStatusFailed:
			return nil, fmt.Errorf("job failed: %s", job.Error)

		case storage.JobStatusCanceled:
			return nil, fmt.Errorf("job was canceled")
		}

		time.Sleep(pollInterval)
	}
}

// enqueueReview enqueues a review job and returns the job ID
func enqueueReview(repoPath, gitRef, agentName string) (int64, error) {
	addr := getDaemonAddr()

	reqBody, _ := json.Marshal(daemon.EnqueueRequest{
		RepoPath: repoPath,
		GitRef:   gitRef,
		Agent:    agentName,
	})

	resp, err := http.Post(addr+"/api/enqueue", "application/json", bytes.NewReader(reqBody))
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

// getCommentsForJob fetches comments for a job
func getCommentsForJob(jobID int64) ([]storage.Response, error) {
	addr := getDaemonAddr()
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(fmt.Sprintf("%s/api/comments?job_id=%d", addr, jobID))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch comments: %s", resp.Status)
	}

	var result struct {
		Responses []storage.Response `json:"responses"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Responses, nil
}
