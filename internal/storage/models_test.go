package storage_test

import (
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

func TestIsTaskJob(t *testing.T) {
	tests := []struct {
		name string
		job  storage.ReviewJob
		want bool
	}{
		// Explicit JobType
		{
			name: "explicit: single commit review by job_type",
			job:  storage.ReviewJob{JobType: storage.JobTypeReview, CommitID: testutil.Ptr(int64(1)), GitRef: "abc123"},
			want: false,
		},
		{
			name: "explicit: dirty review by job_type",
			job:  storage.ReviewJob{JobType: storage.JobTypeDirty, GitRef: "dirty"},
			want: false,
		},
		{
			name: "explicit: dirty review by job_type with diff content",
			job:  storage.ReviewJob{JobType: storage.JobTypeDirty, GitRef: "dirty", DiffContent: testutil.Ptr("diff")},
			want: false,
		},
		{
			name: "explicit: range review by job_type",
			job:  storage.ReviewJob{JobType: storage.JobTypeRange, GitRef: "abc123..def456"},
			want: false,
		},
		{
			name: "explicit: task job by job_type",
			job:  storage.ReviewJob{JobType: storage.JobTypeTask, GitRef: "run:lint"},
			want: true,
		},
		{
			name: "explicit: task job analyze by job_type",
			job:  storage.ReviewJob{JobType: storage.JobTypeTask, GitRef: "analyze"},
			want: true,
		},
		// Inferred JobType
		{
			name: "inferred: single commit review",
			job:  storage.ReviewJob{CommitID: testutil.Ptr(int64(1)), GitRef: "abc123"},
			want: false,
		},
		{
			name: "inferred: dirty review",
			job:  storage.ReviewJob{GitRef: "dirty"},
			want: false,
		},
		{
			name: "inferred: dirty review with diff content",
			job:  storage.ReviewJob{GitRef: "dirty", DiffContent: testutil.Ptr("diff")},
			want: false,
		},
		{
			name: "inferred: branch range review",
			job:  storage.ReviewJob{GitRef: "abc123..def456"},
			want: false,
		},
		{
			name: "inferred: triple-dot range review",
			job:  storage.ReviewJob{GitRef: "main...feature"},
			want: false,
		},
		{
			name: "inferred: task job with label",
			job:  storage.ReviewJob{GitRef: "run:lint"},
			want: true,
		},
		{
			name: "inferred: task job analyze",
			job:  storage.ReviewJob{GitRef: "analyze"},
			want: true,
		},
		{
			name: "inferred: task job run",
			job:  storage.ReviewJob{GitRef: "run"},
			want: true,
		},
		{
			name: "inferred: empty git ref",
			job:  storage.ReviewJob{GitRef: ""},
			want: false,
		},
	}

	for _, tt := range tests {
		if tt.job.IsTaskJob() != tt.want {
			t.Errorf("%q: IsTaskJob() = %v, want %v", tt.name, tt.job.IsTaskJob(), tt.want)
		}
	}
}

func TestIsDirtyJob(t *testing.T) {
	tests := []struct {
		name string
		job  storage.ReviewJob
		want bool
	}{
		// Explicit JobType
		{
			name: "explicit: dirty by job_type",
			job:  storage.ReviewJob{JobType: storage.JobTypeDirty, GitRef: "dirty"},
			want: true,
		},
		{
			name: "explicit: review by job_type",
			job:  storage.ReviewJob{JobType: storage.JobTypeReview, GitRef: "abc123"},
			want: false,
		},
		{
			name: "explicit: task by job_type",
			job:  storage.ReviewJob{JobType: storage.JobTypeTask, GitRef: "run"},
			want: false,
		},
		// Inferred JobType
		{
			name: "inferred: git_ref dirty",
			job:  storage.ReviewJob{GitRef: "dirty"},
			want: true,
		},
		{
			name: "inferred: diff content set",
			job:  storage.ReviewJob{GitRef: "some-ref", DiffContent: testutil.Ptr("diff")},
			want: true,
		},
		{
			name: "inferred: normal commit",
			job:  storage.ReviewJob{GitRef: "abc123", CommitID: testutil.Ptr(int64(1))},
			want: false,
		},
		{
			name: "inferred: range",
			job:  storage.ReviewJob{GitRef: "abc..def"},
			want: false,
		},
	}

	for _, tt := range tests {
		if tt.job.IsDirtyJob() != tt.want {
			t.Errorf("%q: IsDirtyJob() = %v, want %v", tt.name, tt.job.IsDirtyJob(), tt.want)
		}
	}
}

func TestUsesStoredPrompt(t *testing.T) {
	tests := []struct {
		name string
		job  storage.ReviewJob
		want bool
	}{
		{name: "task", job: storage.ReviewJob{JobType: storage.JobTypeTask}, want: true},
		{name: "compact", job: storage.ReviewJob{JobType: storage.JobTypeCompact}, want: true},
		{name: "review", job: storage.ReviewJob{JobType: storage.JobTypeReview}, want: false},
		{name: "range", job: storage.ReviewJob{JobType: storage.JobTypeRange}, want: false},
		{name: "dirty", job: storage.ReviewJob{JobType: storage.JobTypeDirty}, want: false},
		{name: "empty", job: storage.ReviewJob{JobType: ""}, want: false},
		{name: "security", job: storage.ReviewJob{JobType: "security"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.job.UsesStoredPrompt(); got != tt.want {
				t.Errorf("UsesStoredPrompt() = %v, want %v", got, tt.want)
			}
		})
	}
}
