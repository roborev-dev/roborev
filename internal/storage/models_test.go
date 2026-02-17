package storage_test

import (
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
)

func TestIsTaskJob(t *testing.T) {
	t.Run("Explicit JobType", func(t *testing.T) {
		tests := []struct {
			name string
			job  storage.ReviewJob
			want bool
		}{
			{
				name: "single commit review by job_type",
				job:  storage.ReviewJob{JobType: storage.JobTypeReview, CommitID: testutil.Ptr(int64(1)), GitRef: "abc123"},
				want: false,
			},
			{
				name: "dirty review by job_type",
				job:  storage.ReviewJob{JobType: storage.JobTypeDirty, GitRef: "dirty"},
				want: false,
			},
			{
				name: "dirty review by job_type with diff content",
				job:  storage.ReviewJob{JobType: storage.JobTypeDirty, GitRef: "dirty", DiffContent: testutil.Ptr("diff")},
				want: false,
			},
			{
				name: "range review by job_type",
				job:  storage.ReviewJob{JobType: storage.JobTypeRange, GitRef: "abc123..def456"},
				want: false,
			},
			{
				name: "task job by job_type",
				job:  storage.ReviewJob{JobType: storage.JobTypeTask, GitRef: "run:lint"},
				want: true,
			},
			{
				name: "task job analyze by job_type",
				job:  storage.ReviewJob{JobType: storage.JobTypeTask, GitRef: "analyze"},
				want: true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := tt.job.IsTaskJob()
				if got != tt.want {
					t.Errorf("IsTaskJob() = %v, want %v", got, tt.want)
				}
			})
		}
	})

	t.Run("Inferred JobType", func(t *testing.T) {
		tests := []struct {
			name string
			job  storage.ReviewJob
			want bool
		}{
			{
				name: "fallback: single commit review",
				job:  storage.ReviewJob{CommitID: testutil.Ptr(int64(1)), GitRef: "abc123"},
				want: false,
			},
			{
				name: "fallback: dirty review",
				job:  storage.ReviewJob{GitRef: "dirty"},
				want: false,
			},
			{
				name: "fallback: dirty review with diff content",
				job:  storage.ReviewJob{GitRef: "dirty", DiffContent: testutil.Ptr("diff")},
				want: false,
			},
			{
				name: "fallback: branch range review",
				job:  storage.ReviewJob{GitRef: "abc123..def456"},
				want: false,
			},
			{
				name: "fallback: triple-dot range review",
				job:  storage.ReviewJob{GitRef: "main...feature"},
				want: false,
			},
			{
				name: "fallback: task job with label",
				job:  storage.ReviewJob{GitRef: "run:lint"},
				want: true,
			},
			{
				name: "fallback: task job analyze",
				job:  storage.ReviewJob{GitRef: "analyze"},
				want: true,
			},
			{
				name: "fallback: task job run",
				job:  storage.ReviewJob{GitRef: "run"},
				want: true,
			},
			{
				name: "fallback: empty git ref",
				job:  storage.ReviewJob{GitRef: ""},
				want: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := tt.job.IsTaskJob()
				if got != tt.want {
					t.Errorf("IsTaskJob() = %v, want %v", got, tt.want)
				}
			})
		}
	})
}

func TestIsDirtyJob(t *testing.T) {
	t.Run("Explicit JobType", func(t *testing.T) {
		tests := []struct {
			name string
			job  storage.ReviewJob
			want bool
		}{
			{
				name: "dirty by job_type",
				job:  storage.ReviewJob{JobType: storage.JobTypeDirty, GitRef: "dirty"},
				want: true,
			},
			{
				name: "review by job_type",
				job:  storage.ReviewJob{JobType: storage.JobTypeReview, GitRef: "abc123"},
				want: false,
			},
			{
				name: "task by job_type",
				job:  storage.ReviewJob{JobType: storage.JobTypeTask, GitRef: "run"},
				want: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := tt.job.IsDirtyJob()
				if got != tt.want {
					t.Errorf("IsDirtyJob() = %v, want %v", got, tt.want)
				}
			})
		}
	})

	t.Run("Inferred JobType", func(t *testing.T) {
		tests := []struct {
			name string
			job  storage.ReviewJob
			want bool
		}{
			{
				name: "fallback: git_ref dirty",
				job:  storage.ReviewJob{GitRef: "dirty"},
				want: true,
			},
			{
				name: "fallback: diff content set",
				job:  storage.ReviewJob{GitRef: "some-ref", DiffContent: testutil.Ptr("diff")},
				want: true,
			},
			{
				name: "fallback: normal commit",
				job:  storage.ReviewJob{GitRef: "abc123", CommitID: testutil.Ptr(int64(1))},
				want: false,
			},
			{
				name: "fallback: range",
				job:  storage.ReviewJob{GitRef: "abc..def"},
				want: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := tt.job.IsDirtyJob()
				if got != tt.want {
					t.Errorf("IsDirtyJob() = %v, want %v", got, tt.want)
				}
			})
		}
	})
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
