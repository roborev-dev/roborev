package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCIReviewCmd_Help(t *testing.T) {
	cmd := ciCmd()
	cmd.SetArgs([]string{"review", "--help"})

	// Capture output
	var buf strings.Builder
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	checks := []string{
		"--ref",
		"--comment",
		"--gh-repo",
		"--pr",
		"--agent",
		"--review-types",
		"--reasoning",
		"--min-severity",
		"--synthesis-agent",
	}
	for _, check := range checks {
		assert.Contains(t, output, check, "unexpected condition")
	}
}

func TestCIReviewCmd_Validation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	tests := []struct {
		name      string
		args      []string
		wantError string
		clearEnv  bool
	}{
		{"InvalidReviewType", []string{"review", "--ref", "abc", "--review-types", "bogus"}, "invalid review_type", false},
		{"InvalidReasoning", []string{"review", "--ref", "abc", "--reasoning", "bogus"}, "invalid reasoning", false},
		{"InvalidMinSeverity", []string{"review", "--ref", "abc", "--min-severity", "bogus"}, "invalid min_severity", false},
		{"RequiresRef", []string{"review"}, "auto-detection", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.clearEnv {
				t.Setenv("GITHUB_EVENT_PATH", "")
				t.Setenv("GITHUB_REF", "")
			}
			cmd := ciCmd()
			cmd.SetArgs(tt.args)
			err := cmd.Execute()

			require.Error(t, err)

			if !strings.Contains(err.Error(), tt.wantError) {
				assert.Errorf(t, err, "expected error containing %q, got: %v", tt.wantError, err)
			}
		})
	}
}

func setupFakeGitHubEvent(t *testing.T, event map[string]any) {
	t.Helper()
	eventFile := filepath.Join(t.TempDir(), "event.json")
	data, _ := json.Marshal(event)
	if err := os.WriteFile(eventFile, data, 0644); err != nil {
		require.NoError(t, err)
	}
	t.Setenv("GITHUB_EVENT_PATH", eventFile)
}

func TestDetectGitRef(t *testing.T) {
	setupFakeGitHubEvent(t, map[string]any{
		"pull_request": map[string]any{
			"base": map[string]string{
				"sha": "aaa111",
			},
			"head": map[string]string{
				"sha": "bbb222",
			},
		},
	})

	ref, err := detectGitRef()
	require.NoError(t, err)

	assert.Equal(t, "aaa111..bbb222", ref, "unexpected condition")
}

func TestDetectGitRef_NoEnv(t *testing.T) {
	t.Setenv("GITHUB_EVENT_PATH", "")

	_, err := detectGitRef()
	require.Error(t, err)

}

func TestDetectPRNumber_EventJSON(t *testing.T) {
	setupFakeGitHubEvent(t, map[string]any{
		"pull_request": map[string]any{
			"number": 42,
		},
	})

	pr, err := detectPRNumber()
	require.NoError(t, err)

	assert.Equal(t, 42, pr, "unexpected condition")
}

func TestDetectPRNumber_GitHubRef(t *testing.T) {
	t.Setenv("GITHUB_EVENT_PATH", "")
	t.Setenv("GITHUB_REF", "refs/pull/123/merge")

	pr, err := detectPRNumber()
	require.NoError(t, err)

	assert.Equal(t, 123, pr, "unexpected condition")
}

func TestDetectPRNumber_NoEnv(t *testing.T) {
	t.Setenv("GITHUB_EVENT_PATH", "")
	t.Setenv("GITHUB_REF", "")

	_, err := detectPRNumber()
	require.Error(t, err)

}

func TestExtractHeadSHA(t *testing.T) {
	tests := []struct {
		ref  string
		want string
	}{
		{"aaa..bbb", "bbb"},
		{"abc123", "abc123"},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractHeadSHA(tt.ref)
		assert.Equal(t, tt.want, got, "unexpected condition")
	}
}

func TestResolveAgentList(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		agents := resolveAgentList(
			"codex,gemini", nil, nil)
		assert.False(t, len(agents) != 2 ||
			agents[0] != "codex" ||
			agents[1] != "gemini", "unexpected condition")
	})

	t.Run("default", func(t *testing.T) {
		agents := resolveAgentList("", nil, nil)
		assert.False(t, len(agents) != 1 || agents[0] != "", "unexpected condition")
	})
}

func TestResolveReviewTypes(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		types := resolveReviewTypes(
			"security,design", nil, nil)
		assert.Len(t, types, 2, "unexpected condition")
	})

	t.Run("default", func(t *testing.T) {
		types := resolveReviewTypes("", nil, nil)
		assert.False(t, len(types) != 1 || types[0] != "security", "unexpected condition")
	})
}

func TestResolveAgentList_EmptyFlag(t *testing.T) {
	// Comma-only flag should resolve to empty list.
	agents := resolveAgentList(",", nil, nil)
	assert.Empty(t, agents, "unexpected condition")
}

func TestResolveReviewTypes_EmptyFlag(t *testing.T) {
	// Whitespace-comma flag should resolve to empty list.
	types := resolveReviewTypes(" , ", nil, nil)
	assert.Empty(t, types, "unexpected condition")
}

func boolPtr(v bool) *bool { return &v }

func TestResolveCIUpsertComments(t *testing.T) {
	tests := []struct {
		name   string
		repo   *config.RepoConfig
		global *config.Config
		want   bool
	}{
		{
			name: "nil/nil defaults to false",
			repo: nil, global: nil, want: false,
		},
		{
			name:   "global true",
			repo:   nil,
			global: &config.Config{CI: config.CIConfig{UpsertComments: true}},
			want:   true,
		},
		{
			name:   "global false",
			repo:   nil,
			global: &config.Config{CI: config.CIConfig{UpsertComments: false}},
			want:   false,
		},
		{
			name: "repo true overrides global false",
			repo: &config.RepoConfig{
				CI: config.RepoCIConfig{UpsertComments: boolPtr(true)},
			},
			global: &config.Config{CI: config.CIConfig{UpsertComments: false}},
			want:   true,
		},
		{
			name: "repo false overrides global true",
			repo: &config.RepoConfig{
				CI: config.RepoCIConfig{UpsertComments: boolPtr(false)},
			},
			global: &config.Config{CI: config.CIConfig{UpsertComments: true}},
			want:   false,
		},
		{
			name: "repo nil falls through to global",
			repo: &config.RepoConfig{
				CI: config.RepoCIConfig{UpsertComments: nil},
			},
			global: &config.Config{CI: config.CIConfig{UpsertComments: true}},
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveCIUpsertComments(tt.repo, tt.global)
			assert.Equal(t, tt.want, got, "unexpected condition")
		})
	}
}

func TestSplitTrimmed(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{"a, b , c", []string{"a", "b", "c"}},
		{"single", []string{"single"}},
		{" , , ", nil},
	}
	for _, tt := range tests {
		got := splitTrimmed(tt.in)
		assert.Len(t, tt.want, len(got), "unexpected condition")
		for i := range got {
			assert.Equal(t, got[i], tt.want[i], "unexpected condition")
		}
	}
}
