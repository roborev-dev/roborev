// ABOUTME: Shared helper functions for job polling and completion
// ABOUTME: Consolidates duplicate polling logic from compact, analyze, fix, and run commands
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
)

// waitForJobCompletion polls a job until it completes, streaming output if provided.
// This consolidates polling logic used across compact, analyze, fix, and run commands.
func waitForJobCompletion(ctx context.Context, serverAddr string, jobID int64, output io.Writer) (*storage.Review, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	pollInterval := 1 * time.Second
	maxInterval := 5 * time.Second
	lastOutputLen := 0
	lastStatus := ""
	waitDots := 0

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}

		// Check job status
		req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/jobs?id=%d", serverAddr, jobID), nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			continue // Keep polling
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var jobsResp struct {
			Jobs []storage.ReviewJob `json:"jobs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&jobsResp); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		if len(jobsResp.Jobs) == 0 {
			return nil, fmt.Errorf("job %d not found", jobID)
		}

		job := jobsResp.Jobs[0]

		// Show status progress indicator while waiting
		if output != nil && lastStatus != string(job.Status) {
			lastStatus = string(job.Status)
			switch job.Status {
			case storage.JobStatusQueued:
				fmt.Fprint(output, "Waiting for agent to start")
			case storage.JobStatusRunning:
				fmt.Fprint(output, "\nAgent processing")
			}
			waitDots = 0
		}

		// Show dots while waiting (before any output appears)
		if output != nil && job.Status == storage.JobStatusQueued {
			waitDots++
			if waitDots%2 == 0 { // Show dot every 2 seconds
				fmt.Fprint(output, ".")
			}
		}

		if output != nil && job.Status == storage.JobStatusRunning && lastOutputLen == 0 {
			waitDots++
			if waitDots%2 == 0 {
				fmt.Fprint(output, ".")
			}
		}

		// Stream partial output while running
		if output != nil && job.Status == storage.JobStatusRunning {
			reviewReq, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/review?job_id=%d", serverAddr, jobID), nil)
			if err == nil {
				reviewResp, err := client.Do(reviewReq)
				if err == nil {
					if reviewResp.StatusCode == http.StatusOK {
						var review storage.Review
						if json.NewDecoder(reviewResp.Body).Decode(&review) == nil {
							if len(review.Output) > lastOutputLen {
								// First output - add newline after waiting dots
								if lastOutputLen == 0 && waitDots > 0 {
									fmt.Fprintln(output)
								}
								fmt.Fprint(output, review.Output[lastOutputLen:])
								lastOutputLen = len(review.Output)
							}
						}
					}
					reviewResp.Body.Close()
				}
			}
		}

		switch job.Status {
		case storage.JobStatusDone:
			// Fetch final review
			reviewReq, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/review?job_id=%d", serverAddr, jobID), nil)
			if err != nil {
				return nil, fmt.Errorf("create review request: %w", err)
			}

			reviewResp, err := client.Do(reviewReq)
			if err != nil {
				return nil, fmt.Errorf("fetch review: %w", err)
			}
			defer reviewResp.Body.Close()

			if reviewResp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(reviewResp.Body)
				return nil, fmt.Errorf("fetch review (%d): %s", reviewResp.StatusCode, body)
			}

			var review storage.Review
			if err := json.NewDecoder(reviewResp.Body).Decode(&review); err != nil {
				return nil, fmt.Errorf("parse review: %w", err)
			}

			// Print any remaining output
			if output != nil && len(review.Output) > lastOutputLen {
				fmt.Fprint(output, review.Output[lastOutputLen:])
			}

			return &review, nil

		case storage.JobStatusFailed:
			return nil, fmt.Errorf("job failed: %s", job.Error)

		case storage.JobStatusCanceled:
			return nil, fmt.Errorf("job was canceled")
		}

		// Exponential backoff
		if pollInterval < maxInterval {
			pollInterval = pollInterval * 3 / 2
			if pollInterval > maxInterval {
				pollInterval = maxInterval
			}
		}
	}
}
