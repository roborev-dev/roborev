package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	data, _ := json.Marshal(event)
	if err := os.WriteFile(eventFile, data, 0644); err != nil {
		t.Fatal(err)
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref != "aaa111..bbb222" {
		t.Errorf("ref = %q, want %q",
			ref, "aaa111..bbb222")
	}
}

func TestDetectGitRef_NoEnv(t *testing.T) {
	t.Setenv("GITHUB_EVENT_PATH", "")

	_, err := detectGitRef()
	if err == nil {
		t.Fatal("expected error when no env set")
	}
}

func TestDetectPRNumber_EventJSON(t *testing.T) {
	setupFakeGitHubEvent(t, map[string]any{
		"pull_request": map[string]any{
			"number": 42,
		},
	})

	pr, err := detectPRNumber()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pr != 42 {
		t.Errorf("pr = %d, want 42", pr)
	}
}

func TestDetectPRNumber_GitHubRef(t *testing.T) {
	t.Setenv("GITHUB_EVENT_PATH", "")
	t.Setenv("GITHUB_REF", "refs/pull/123/merge")

	pr, err := detectPRNumber()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pr != 123 {
		t.Errorf("pr = %d, want 123", pr)
	}
}

func TestDetectPRNumber_NoEnv(t *testing.T) {
	t.Setenv("GITHUB_EVENT_PATH", "")
	t.Setenv("GITHUB_REF", "")

	_, err := detectPRNumber()
	if err == nil {
		t.Fatal("expected error when no env set")
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
		if len(agents) != 2 ||
			agents[0] != "codex" ||
			agents[1] != "gemini" {
			t.Errorf("got %v", agents)
		}
	})

	t.Run("default", func(t *testing.T) {
		agents := resolveAgentList("", nil, nil)
		if len(agents) != 1 || agents[0] != "" {
			t.Errorf("got %v, want [\"\"]", agents)
		}
	})
}

func TestResolveReviewTypes(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		types := resolveReviewTypes(
			"security,design", nil, nil)
		if len(types) != 2 {
			t.Errorf("got %v", types)
		}
	})

	t.Run("default", func(t *testing.T) {
		types := resolveReviewTypes("", nil, nil)
		if len(types) != 1 || types[0] != "security" {
			t.Errorf(
				"got %v, want [security]", types)
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
		if len(got) != len(tt.want) {
			t.Errorf(
				"splitTrimmed(%q) = %v, want %v",
				tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf(
					"splitTrimmed(%q)[%d] = %q, "+
						"want %q",
					tt.in, i, got[i], tt.want[i])
			}
		}
	}
}
