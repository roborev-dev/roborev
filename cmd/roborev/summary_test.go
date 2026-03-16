package main

import (
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/stretchr/testify/assert"
)

func TestRepoLabels(t *testing.T) {
	tests := []struct {
		name  string
		repos []storage.RepoSummary
		want  []string
	}{
		{
			name: "unique basenames",
			repos: []storage.RepoSummary{
				{Path: "/home/user/project-a"},
				{Path: "/home/user/project-b"},
			},
			want: []string{"project-a", "project-b"},
		},
		{
			name: "duplicate basenames different parents",
			repos: []storage.RepoSummary{
				{Path: "/home/user/code/roborev"},
				{Path: "/home/user/worktrees/roborev"},
			},
			want: []string{"code/roborev", "worktrees/roborev"},
		},
		{
			name: "duplicate basenames same parent",
			repos: []storage.RepoSummary{
				{Path: "/a/worktrees/roborev"},
				{Path: "/b/worktrees/roborev"},
			},
			want: []string{"a/worktrees/roborev", "b/worktrees/roborev"},
		},
		{
			name: "uses Name field when set",
			repos: []storage.RepoSummary{
				{Name: "my-repo", Path: "/home/user/my-repo"},
				{Path: "/home/user/other-repo"},
			},
			want: []string{"my-repo", "other-repo"},
		},
		{
			name: "single repo",
			repos: []storage.RepoSummary{
				{Path: "/home/user/project"},
			},
			want: []string{"project"},
		},
		{
			name: "three duplicates",
			repos: []storage.RepoSummary{
				{Path: "/x/a/repo"},
				{Path: "/y/a/repo"},
				{Path: "/z/b/repo"},
			},
			want: []string{"x/a/repo", "y/a/repo", "b/repo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := repoLabels(tt.repos)
			assert.Equal(t, tt.want, got)
		})
	}
}
