//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/prompt/analyze"
	"github.com/roborev-dev/roborev/internal/storage"
)

type jobResponse struct {
	status   string
	review   string
	errMsg   string
	notFound bool
}

const testJobID = 42

type jobMockServer struct {
	responses []jobResponse
	pollCount int32
}

func (m *jobMockServer) handleJobs(w http.ResponseWriter, r *http.Request) {
	if len(m.responses) == 0 {
		http.Error(w, "no mock responses configured", http.StatusInternalServerError)
		return
	}
	idx := int(atomic.AddInt32(&m.pollCount, 1)) - 1
	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	}
	resp := m.responses[idx]

	if resp.notFound {
		if err := json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []interface{}{}}); err != nil {
			http.Error(w, "mock encoding error", http.StatusInternalServerError)
		}
		return
	}
	job := storage.ReviewJob{
		ID:     testJobID,
		Status: storage.JobStatus(resp.status),
		Error:  resp.errMsg,
	}
	if err := json.NewEncoder(w).Encode(map[string]interface{}{"jobs": []storage.ReviewJob{job}}); err != nil {
		http.Error(w, "mock encoding error", http.StatusInternalServerError)
	}
}

func (m *jobMockServer) handleReview(w http.ResponseWriter, r *http.Request) {
	if len(m.responses) == 0 {
		http.Error(w, "no mock responses configured", http.StatusInternalServerError)
		return
	}
	// Return the final review state without incrementing the poll count
	resp := m.responses[len(m.responses)-1]

	if err := json.NewEncoder(w).Encode(storage.Review{
		JobID:  testJobID,
		Output: resp.review,
	}); err != nil {
		http.Error(w, "mock encoding error", http.StatusInternalServerError)
	}
}

func TestWaitForAnalysisJob(t *testing.T) {
	tests := []struct {
		name       string
		responses  []jobResponse // sequence of responses
		wantErr    bool
		wantErrMsg string
	}{
		{
			name: "immediate success",
			responses: []jobResponse{
				{status: "done", review: "Analysis complete: found 3 issues"},
			},
		},
		{
			name: "queued then done",
			responses: []jobResponse{
				{status: "queued"},
				{status: "running"},
				{status: "done", review: "All good"},
			},
		},
		{
			name: "job failed",
			responses: []jobResponse{
				{status: "failed", errMsg: "agent crashed"},
			},
			wantErr:    true,
			wantErrMsg: "agent crashed",
		},
		{
			name: "job canceled",
			responses: []jobResponse{
				{status: "canceled"},
			},
			wantErr:    true,
			wantErrMsg: "canceled",
		},
		{
			name: "job not found",
			responses: []jobResponse{
				{notFound: true},
			},
			wantErr:    true,
			wantErrMsg: "not found",
		},
		{
			name:       "empty responses",
			responses:  []jobResponse{},
			wantErr:    true,
			wantErrMsg: "no mock responses configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &jobMockServer{responses: tt.responses}
			mux := http.NewServeMux()
			mux.HandleFunc("/api/jobs", mock.handleJobs)
			mux.HandleFunc("/api/review", mock.handleReview)
			ts := httptest.NewServer(mux)
			defer ts.Close()

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
			if review.Output != tt.responses[len(tt.responses)-1].review {
				t.Errorf("got review %q, want %q", review.Output, tt.responses[len(tt.responses)-1].review)
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

	cmd, output := newTestCmd(t)

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
	if atomic.LoadInt32(&state.JobsCount) < 2 {
		t.Error("should have polled for job status")
	}
	if atomic.LoadInt32(&state.ReviewCount) == 0 {
		t.Error("should have fetched the review")
	}
	if atomic.LoadInt32(&state.AddressCount) == 0 {
		t.Error("should have marked job as addressed")
	}

	// Verify output contains analysis result
	outputStr := output.String()
	if !strings.Contains(outputStr, "CODE SMELLS") {
		t.Error("output should contain analysis result")
	}
	if !strings.Contains(outputStr, "marked as addressed") {
		t.Error("output should confirm job was addressed")
	}
}
