// ABOUTME: Unit tests for the compact command
// ABOUTME: Tests validation, prompt building, and helper functions
package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
)

func TestIsValidConsolidatedReview(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "valid_with_findings",
			output: "# Review Summary\n\n## Critical Issues\n\n1. SQL injection in main.go:42",
			want:   true,
		},
		{
			name:   "valid_all_addressed",
			output: "All previous findings have been addressed.",
			want:   true,
		},
		{
			name:   "invalid_empty",
			output: "",
			want:   false,
		},
		{
			name:   "invalid_whitespace_only",
			output: "   \n\t  ",
			want:   false,
		},
		{
			name:   "invalid_error_at_start",
			output: "Error: failed to read file main.go",
			want:   false,
		},
		{
			name:   "invalid_exception_at_start",
			output: "Exception: cannot connect to database",
			want:   false,
		},
		{
			name:   "valid_error_in_content",
			output: "## Findings\n\nFixed the error: Cannot reproduce issue. High severity in main.go:10",
			want:   true,
		},
		{
			name:   "valid_cannot_in_content",
			output: "## Issues\n\nThe code cannot handle null values. Medium severity. See utils.go:42",
			want:   true,
		},
		{
			name:   "valid_with_severity_and_structure",
			output: "## High Severity Issues\n\nBuffer overflow found in authentication",
			want:   true,
		},
		{
			name:   "valid_consolidated_review",
			output: "## VERIFIED FINDINGS\n\n### **High Severity**\n\n#### 1. SQL Injection\n**Files:** main.go:42\n**Issue:** User input not sanitized",
			want:   true,
		},
		{
			name:   "valid_with_critical",
			output: "## Critical Issues\n\nBuffer overflow detected in authentication",
			want:   true,
		},
		{
			name:   "valid_with_medium",
			output: "## Medium Severity\n\nImprove error handling in parser",
			want:   true,
		},
		{
			name:   "valid_with_low",
			output: "## Low Priority Issues\n\nConsider adding documentation",
			want:   true,
		},
		{
			name:   "valid_with_go_file_reference",
			output: "## Issues\n\nMemory leak in main.go:123",
			want:   true,
		},
		{
			name:   "valid_with_py_file_reference",
			output: "## Findings\n\nLogic error in script.py:45",
			want:   true,
		},
		{
			name:   "invalid_traceback",
			output: "Traceback (most recent call last):\n  File main.py",
			want:   false,
		},
		{
			name:   "valid_plain_text_no_structure",
			output: "No remaining issues found. The codebase looks clean.",
			want:   true,
		},
		{
			name:   "valid_alternative_wording",
			output: "All findings have been resolved in the current codebase.",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidConsolidatedReview(tt.output)
			if got != tt.want {
				t.Errorf("isValidConsolidatedReview(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestFilterReviewJobs(t *testing.T) {
	tests := []struct {
		name    string
		jobs    []storage.ReviewJob
		wantIDs []int64
	}{
		{
			name: "excludes_compact_and_task",
			jobs: []storage.ReviewJob{
				{ID: 1, JobType: "review"},
				{ID: 2, JobType: "compact"},
				{ID: 3, JobType: "range"},
				{ID: 4, JobType: "task"},
				{ID: 5, JobType: "dirty"},
			},
			wantIDs: []int64{1, 3, 5},
		},
		{
			name: "keeps_all_review_types",
			jobs: []storage.ReviewJob{
				{ID: 10, JobType: "review"},
				{ID: 11, JobType: "range"},
				{ID: 12, JobType: "dirty"},
				{ID: 13, JobType: "security"},
			},
			wantIDs: []int64{10, 11, 12, 13},
		},
		{
			name:    "empty_input",
			jobs:    []storage.ReviewJob{},
			wantIDs: []int64{},
		},
		{
			name: "all_excluded",
			jobs: []storage.ReviewJob{
				{ID: 1, JobType: "compact"},
				{ID: 2, JobType: "task"},
			},
			wantIDs: []int64{},
		},
		{
			name: "empty_job_type_kept",
			jobs: []storage.ReviewJob{
				{ID: 1, JobType: ""},
				{ID: 2, JobType: "compact"},
			},
			wantIDs: []int64{1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterReviewJobs(tt.jobs)

			var gotIDs []int64
			for _, j := range got {
				gotIDs = append(gotIDs, j.ID)
			}

			if !slices.Equal(gotIDs, tt.wantIDs) {
				t.Errorf("filterReviewJobs() = %v, want %v", gotIDs, tt.wantIDs)
			}
		})
	}
}

func TestExtractJobIDs(t *testing.T) {
	tests := []struct {
		name    string
		reviews []jobReview
		want    []int64
	}{
		{
			name: "three_jobs",
			reviews: []jobReview{
				{jobID: 123},
				{jobID: 456},
				{jobID: 789},
			},
			want: []int64{123, 456, 789},
		},
		{
			name:    "empty",
			reviews: []jobReview{},
			want:    []int64{},
		},
		{
			name: "single_job",
			reviews: []jobReview{
				{jobID: 999},
			},
			want: []int64{999},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJobIDs(tt.reviews)

			if !slices.Equal(got, tt.want) {
				t.Errorf("extractJobIDs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func mockJobReview(id int64, ref, output string) jobReview {
	return jobReview{
		jobID:  id,
		job:    &storage.ReviewJob{ID: id, GitRef: ref},
		review: &storage.Review{Output: output},
	}
}

func TestBuildCompactPrompt(t *testing.T) {
	tests := []struct {
		name           string
		jobReviews     []jobReview
		branch         string
		wantContains   []string
		wantNotContain []string
	}{
		{
			name: "single_job_no_branch",
			jobReviews: []jobReview{
				mockJobReview(123, "abc123def456", "Finding 1: Issue in main.go"),
			},
			branch: "",
			wantContains: []string{
				"Verification and Consolidation Request",
				"1 unaddressed review",
				"Job 123",
				"Finding 1: Issue in main.go",
				"abc123d", // short SHA
			},
		},
		{
			name: "multiple_jobs_with_branch",
			jobReviews: []jobReview{
				mockJobReview(123, "sha1", "Issue 1"),
				mockJobReview(124, "sha2", "Issue 2"),
			},
			branch: "main",
			wantContains: []string{
				"2 unaddressed reviews from branch main",
				"Job 123",
				"Job 124",
				"Issue 1",
				"Issue 2",
			},
		},
		{
			name: "all_branches",
			jobReviews: []jobReview{
				mockJobReview(100, "", "Finding"),
			},
			branch: "",
			wantContains: []string{
				"1 unaddressed review",
			},
			wantNotContain: []string{
				"from branch",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCompactPrompt(tt.jobReviews, tt.branch, "")

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("buildCompactPrompt() missing %q\nGot:\n%s", want, got)
				}
			}

			for _, notWant := range tt.wantNotContain {
				if strings.Contains(got, notWant) {
					t.Errorf("buildCompactPrompt() should not contain %q\nGot:\n%s", notWant, got)
				}
			}
		})
	}
}

func TestBuildCompactOutputPrefix(t *testing.T) {
	tests := []struct {
		name         string
		jobCount     int
		branch       string
		jobIDs       []int64
		wantContains []string
	}{
		{
			name:     "with_branch",
			jobCount: 3,
			branch:   "main",
			jobIDs:   []int64{123, 124, 125},
			wantContains: []string{
				"Verified and consolidated 3 unaddressed reviews from branch main",
				"Original jobs: 123, 124, 125",
			},
		},
		{
			name:     "without_branch",
			jobCount: 2,
			branch:   "",
			jobIDs:   []int64{100, 200},
			wantContains: []string{
				"Verified and consolidated 2 unaddressed reviews",
				"Original jobs: 100, 200",
			},
		},
		{
			name:     "single_job",
			jobCount: 1,
			branch:   "feature",
			jobIDs:   []int64{999},
			wantContains: []string{
				"Verified and consolidated 1 unaddressed review from branch feature",
				"Original jobs: 999",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCompactOutputPrefix(tt.jobCount, tt.branch, tt.jobIDs)

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("buildCompactOutputPrefix() missing %q\nGot:\n%s", want, got)
				}
			}
		})
	}
}

func TestWriteCompactMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ROBOREV_DATA_DIR", tmpDir)

	tests := []struct {
		name           string
		consolidatedID int64
		sourceIDs      []int64
		expectFile     bool
	}{
		{
			name:           "write_valid_metadata",
			consolidatedID: 999,
			sourceIDs:      []int64{100, 200, 300},
			expectFile:     true,
		},
		{
			name:           "write_empty_source_ids",
			consolidatedID: 888,
			sourceIDs:      []int64{},
			expectFile:     false,
		},
		{
			name:           "write_single_source_id",
			consolidatedID: 777,
			sourceIDs:      []int64{42},
			expectFile:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := writeCompactMetadata(tt.consolidatedID, tt.sourceIDs)
			if err != nil {
				t.Fatalf("writeCompactMetadata failed: %v", err)
			}

			path := filepath.Join(tmpDir, getCompactMetadataFilename(tt.consolidatedID))

			if !tt.expectFile {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("Expected os.ErrNotExist, got: %v", err)
				}
				return
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("Failed to read metadata file: %v", err)
			}

			var metadata struct {
				SourceJobIDs []int64 `json:"source_job_ids"`
			}
			if err := json.Unmarshal(data, &metadata); err != nil {
				t.Fatalf("Failed to parse metadata JSON: %v", err)
			}

			if !slices.Equal(metadata.SourceJobIDs, tt.sourceIDs) {
				t.Errorf("Expected SourceJobIDs %v, got %v", tt.sourceIDs, metadata.SourceJobIDs)
			}
		})
	}
}
