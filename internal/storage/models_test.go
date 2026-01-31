package storage

import "testing"

func TestIsTaskJob(t *testing.T) {
	tests := []struct {
		name string
		job  ReviewJob
		want bool
	}{
		{
			name: "single commit review",
			job:  ReviewJob{CommitID: ptr(int64(1)), GitRef: "abc123"},
			want: false,
		},
		{
			name: "dirty review",
			job:  ReviewJob{GitRef: "dirty"},
			want: false,
		},
		{
			name: "dirty review with diff content",
			job:  ReviewJob{GitRef: "dirty", DiffContent: ptr("diff")},
			want: false,
		},
		{
			name: "branch range review",
			job:  ReviewJob{GitRef: "abc123..def456"},
			want: false,
		},
		{
			name: "triple-dot range review",
			job:  ReviewJob{GitRef: "main...feature"},
			want: false,
		},
		{
			name: "task job with label",
			job:  ReviewJob{GitRef: "run:lint"},
			want: true,
		},
		{
			name: "task job analyze",
			job:  ReviewJob{GitRef: "analyze"},
			want: true,
		},
		{
			name: "task job run",
			job:  ReviewJob{GitRef: "run"},
			want: true,
		},
		{
			name: "empty git ref",
			job:  ReviewJob{GitRef: ""},
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
}

func ptr[T any](v T) *T { return &v }
