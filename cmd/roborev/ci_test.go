package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/config"
)

func TestCIReviewCmd_Help(t *testing.T) {
	cmd := ciCmd()
	cmd.SetArgs([]string{"review", "--help"})

	// Capture output
	var buf strings.Builder
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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
		if !strings.Contains(output, check) {
			t.Errorf("help output missing %q", check)
		}
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
		envPath   string
		envRef    string
	}{
		{"InvalidReviewType", []string{"review", "--ref", "abc", "--review-types", "bogus"}, "invalid review_type", "", ""},
		{"InvalidReasoning", []string{"review", "--ref", "abc", "--reasoning", "bogus"}, "invalid reasoning", "", ""},
		{"InvalidMinSeverity", []string{"review", "--ref", "abc", "--min-severity", "bogus"}, "invalid min_severity", "", ""},
		{"RequiresRef", []string{"review"}, "auto-detection", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GITHUB_EVENT_PATH", tt.envPath)
			t.Setenv("GITHUB_REF", tt.envRef)
			cmd := ciCmd()
			cmd.SetArgs(tt.args)
			err := cmd.Execute()

			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("expected error containing %q, got: %v", tt.wantError, err)
			}
		})
	}
}

func setupFakeGitHubEvent(t *testing.T, event map[string]any) {
	t.Helper()
	eventFile := filepath.Join(t.TempDir(), "event.json")
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal event data: %v", err)
	}
	if err := os.WriteFile(eventFile, data, 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_EVENT_PATH", eventFile)
}

func TestDetectGitRef(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T)
		want    string
		wantErr bool
	}{
		{
			name: "EventJSON",
			setup: func(t *testing.T) {
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
			},
			want: "aaa111..bbb222",
		},
		{
			name: "NoEnv",
			setup: func(t *testing.T) {
				t.Setenv("GITHUB_EVENT_PATH", "")
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			got, err := detectGitRef()
			if (err != nil) != tt.wantErr {
				t.Fatalf("detectGitRef() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("detectGitRef() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectPRNumber(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T)
		want    int
		wantErr bool
	}{
		{
			name: "EventJSON",
			setup: func(t *testing.T) {
				setupFakeGitHubEvent(t, map[string]any{
					"pull_request": map[string]any{
						"number": 42,
					},
				})
			},
			want: 42,
		},
		{
			name: "GitHubRef",
			setup: func(t *testing.T) {
				t.Setenv("GITHUB_EVENT_PATH", "")
				t.Setenv("GITHUB_REF", "refs/pull/123/merge")
			},
			want: 123,
		},
		{
			name: "NoEnv",
			setup: func(t *testing.T) {
				t.Setenv("GITHUB_EVENT_PATH", "")
				t.Setenv("GITHUB_REF", "")
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			got, err := detectPRNumber()
			if (err != nil) != tt.wantErr {
				t.Fatalf("detectPRNumber() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("detectPRNumber() = %d, want %d", got, tt.want)
			}
		})
	}
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
		if got != tt.want {
			t.Errorf(
				"extractHeadSHA(%q) = %q, want %q",
				tt.ref, got, tt.want)
		}
	}
}

func TestResolveAgentList(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		agents := resolveAgentList(
			"codex,gemini", nil, nil)
		want := []string{"codex", "gemini"}
		if !slices.Equal(agents, want) {
			t.Errorf("got %v, want %v", agents, want)
		}
	})

	t.Run("default", func(t *testing.T) {
		agents := resolveAgentList("", nil, nil)
		want := []string{""}
		if !slices.Equal(agents, want) {
			t.Errorf("got %v, want %v", agents, want)
		}
	})
}

func TestResolveReviewTypes(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		types := resolveReviewTypes(
			"security,design", nil, nil)
		want := []string{"security", "design"}
		if !slices.Equal(types, want) {
			t.Errorf("got %v, want %v", types, want)
		}
	})

	t.Run("default", func(t *testing.T) {
		types := resolveReviewTypes("", nil, nil)
		want := []string{"security"}
		if !slices.Equal(types, want) {
			t.Errorf("got %v, want %v", types, want)
		}
	})
}

func TestResolveAgentList_EmptyFlag(t *testing.T) {
	// Comma-only flag should resolve to empty list.
	agents := resolveAgentList(",", nil, nil)
	if len(agents) != 0 {
		t.Errorf(
			"resolveAgentList(\",\") = %v, want empty",
			agents)
	}
}

func TestResolveReviewTypes_EmptyFlag(t *testing.T) {
	// Whitespace-comma flag should resolve to empty list.
	types := resolveReviewTypes(" , ", nil, nil)
	if len(types) != 0 {
		t.Errorf(
			"resolveReviewTypes(\" , \") = %v, want empty",
			types)
	}
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
			if got != tt.want {
				t.Errorf("resolveCIUpsertComments() = %v, want %v", got, tt.want)
			}
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
		if !slices.Equal(got, tt.want) {
			t.Errorf("splitTrimmed(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
