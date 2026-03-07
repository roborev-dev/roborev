//go:build integration

package main

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/prompt/analyze"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestWaitForAnalysisJob(t *testing.T) {
	const testJobID = 42
	tests := []struct {
		name       string
		setupOpts  MockServerOpts
		wantErr    bool
		wantErrMsg string
		wantReview string
	}{
		{
			name: "immediate success",
			setupOpts: MockServerOpts{
				JobIDStart:        testJobID + 1,
				JobStatusSequence: []storage.JobStatus{storage.JobStatusDone},
				ReviewOutput:      "Analysis complete: found 3 issues",
			},
			wantReview: "Analysis complete: found 3 issues",
		},
		{
			name: "queued then done",
			setupOpts: MockServerOpts{
				JobIDStart: testJobID + 1,
				JobStatusSequence: []storage.JobStatus{
					storage.JobStatusQueued,
					storage.JobStatusRunning,
					storage.JobStatusDone,
				},
				ReviewOutput: "All good",
			},
			wantReview: "All good",
		},
		{
			name: "job failed",
			setupOpts: MockServerOpts{
				JobIDStart:        testJobID + 1,
				JobStatusSequence: []storage.JobStatus{storage.JobStatusFailed},
				JobError:          "agent crashed",
			},
			wantErr:    true,
			wantErrMsg: "agent crashed",
		},
		{
			name: "job canceled",
			setupOpts: MockServerOpts{
				JobIDStart:        testJobID + 1,
				JobStatusSequence: []storage.JobStatus{storage.JobStatusCanceled},
			},
			wantErr:    true,
			wantErrMsg: "canceled",
		},
		{
			name: "job not found",
			setupOpts: MockServerOpts{
				JobIDStart:  testJobID + 1,
				JobNotFound: true,
			},
			wantErr:    true,
			wantErrMsg: "not found",
		},
		{
			name: "empty responses",
			setupOpts: MockServerOpts{
				JobIDStart: testJobID + 1,
				OnJobs: func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "no mock responses configured", http.StatusInternalServerError)
				},
			},
			wantErr:    true,
			wantErrMsg: "no mock responses configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, _ := newMockServer(t, tt.setupOpts)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			review, err := waitForAnalysisJob(ctx, ts.URL, testJobID)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrMsg)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if review == nil {
				t.Fatal("expected review, got nil")
			}
			if review.Output != tt.wantReview {
				t.Errorf("got review %q, want %q", review.Output, tt.wantReview)
			}
		})
	}
}

func TestRunAnalyzeAndFix_Integration(t *testing.T) {
	// This tests the full workflow with mocked daemon and test agent
	repo := createTestRepo(t, map[string]string{
		"main.go": "package main\n",
	})

	ts, state := newMockServer(t, MockServerOpts{
		JobIDStart:     99,
		ReviewOutput:   "## CODE SMELLS\n- Found duplicated code in main.go",
		DoneAfterPolls: 2,
	})

	cmd, output := newTestCmd(t, ts.URL)

	analysisType := analyze.GetType("refactor")
	opts := analyzeOptions{
		agentName: "test",
		fix:       true,
		fixAgent:  "test",
		reasoning: "fast",
	}

	err := runAnalyzeAndFix(cmd, ts.URL, repo.Dir, 99, analysisType, opts)
	if err != nil {
		t.Fatalf("runAnalyzeAndFix failed: %v", err)
	}

	// Verify the workflow was executed
	if state.Jobs() < 2 {
		t.Error("should have polled for job status")
	}
	if state.Reviews() == 0 {
		t.Error("should have fetched the review")
	}
	if state.Closes() == 0 {
		t.Error("should have marked job as closed")
	}

	// Verify output contains analysis result
	outputStr := output.String()
	if !strings.Contains(outputStr, "CODE SMELLS") {
		t.Error("output should contain analysis result")
	}
	if !regexp.MustCompile(`Analysis job \d+ closed`).MatchString(outputStr) {
		t.Errorf("output should match 'Analysis job N closed', got: %s", outputStr)
	}
}
