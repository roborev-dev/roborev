package main

// Tests for the show command

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/storage"
)

func TestShowCommandArgParsing(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		setupRepo    func(*TestGitRepo)
		wantQueryHas []string
		wantQueryNot []string
	}{
		{
			name: "numeric ref resolvable in repo treated as SHA not job ID",
			args: []string{"12345"},
			setupRepo: func(r *TestGitRepo) {
				r.Run("tag", "12345")
			},
			wantQueryHas: []string{"sha=<short_sha>"},
			wantQueryNot: []string{"job_id="},
		},
		{
			name:         "numeric non-resolvable treated as job ID",
			args:         []string{"99999"},
			wantQueryHas: []string{"job_id=99999"},
			wantQueryNot: []string{"sha="},
		},
		{
			name:         "non-numeric argument treated as SHA",
			args:         []string{"abc123def"},
			wantQueryHas: []string{"sha=abc123def"},
			wantQueryNot: []string{"job_id="},
		},
		{
			name: "flag --job forces job ID interpretation even when ref is resolvable",
			args: []string{"--job", "12345"},
			setupRepo: func(r *TestGitRepo) {
				r.Run("tag", "12345")
			},
			wantQueryHas: []string{"job_id=12345"},
			wantQueryNot: []string{"sha="},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newTestGitRepo(t)
			commitSHA := repo.CommitFile("file.txt", "content", "initial commit")
			shortSHA := commitSHA[:7]

			if tt.setupRepo != nil {
				tt.setupRepo(repo)
			}

			getQuery := mockReviewDaemon(t, storage.Review{
				ID: 1, JobID: 42, Output: "LGTM", Agent: "test",
			})

			args := append([]string{"--repo", repo.Dir}, tt.args...)
			_, err := runShowCmd(t, args...)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			q := getQuery()
			for _, s := range tt.wantQueryHas {
				s = strings.ReplaceAll(s, "<short_sha>", shortSHA)
				if !strings.Contains(q, s) {
					t.Errorf("expected query to contain %q, got: %s", s, q)
				}
			}
			for _, s := range tt.wantQueryNot {
				s = strings.ReplaceAll(s, "<short_sha>", shortSHA)
				if strings.Contains(q, s) {
					t.Errorf("expected query NOT to contain %q, got: %s", s, q)
				}
			}
		})
	}
}

func TestShowOutputFormat(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantOutput string
		notOutput  string
	}{
		{
			name:       "job ID shows 'Review for job X (by agent)'",
			args:       []string{"--job", "42"},
			wantOutput: "Review for job 42 (by codex)",
			notOutput:  "(job 42, by",
		},
		{
			name:       "SHA shows 'Review for abc123 (job X, by agent)'",
			args:       []string{"<sha>"},
			wantOutput: "Review for <short_sha> (job 42, by codex)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newTestGitRepo(t)
			commitSHA := repo.CommitFile("file.txt", "content", "initial commit")
			shortSHA := commitSHA[:7]

			mockReviewDaemon(t, storage.Review{
				ID: 1, JobID: 42, Output: "Test review output", Agent: "codex",
			})

			args := make([]string, len(tt.args))
			for i, arg := range tt.args {
				args[i] = strings.ReplaceAll(arg, "<sha>", commitSHA)
			}
			args = append([]string{"--repo", repo.Dir}, args...)
			output, err := runShowCmd(t, args...)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			expected := strings.ReplaceAll(tt.wantOutput, "<short_sha>", shortSHA)

			if !strings.Contains(output, expected) {
				t.Errorf("expected %q in output, got: %s", expected, output)
			}
			if tt.notOutput != "" {
				if strings.Contains(output, tt.notOutput) {
					t.Errorf("did not expect %q in output, got: %s", tt.notOutput, output)
				}
			}
		})
	}
}

func TestShowOutsideGitRepo(t *testing.T) {
	t.Run("no args outside git repo returns guidance error", func(t *testing.T) {
		nonGitDir := t.TempDir()

		mockReviewDaemon(t, storage.Review{})

		_, err := runShowCmd(t, "--repo", nonGitDir)
		assertErrorContains(t, err, "not in a git repository")
		assertErrorContains(t, err, "job ID")
	})

	t.Run("job ID outside git repo still works", func(t *testing.T) {
		nonGitDir := t.TempDir()

		getQuery := mockReviewDaemon(t, storage.Review{
			ID: 1, JobID: 42, Output: "LGTM", Agent: "test",
		})

		output, err := runShowCmd(t, "--repo", nonGitDir, "--job", "42")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		q := getQuery()
		if !strings.Contains(q, "job_id=42") {
			t.Errorf("expected job_id=42 in query, got: %s", q)
		}
		if !strings.Contains(output, "LGTM") {
			t.Errorf("expected review output in result, got: %s", output)
		}
	})
}

func TestShowJSONOutput(t *testing.T) {
	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial commit")

	mockReviewDaemon(t, storage.Review{
		ID: 1, JobID: 42, Output: "LGTM", Agent: "test",
	})

	output, err := runShowCmd(t, "--repo", repo.Dir, "--job", "42", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("outputs valid JSON", func(t *testing.T) {
		var parsed storage.Review
		if err := json.Unmarshal([]byte(output), &parsed); err != nil {
			t.Fatalf("--json output not valid JSON: %v\noutput: %s", err, output)
		}
		if parsed.JobID != 42 {
			t.Errorf("expected job_id=42, got %d", parsed.JobID)
		}
		if parsed.Output != "LGTM" {
			t.Errorf("expected output=LGTM, got %q", parsed.Output)
		}
		if parsed.Agent != "test" {
			t.Errorf("expected agent=test, got %q", parsed.Agent)
		}
	})

	t.Run("skips formatted header", func(t *testing.T) {
		if strings.Contains(output, "Review for") {
			t.Errorf("--json should not contain formatted header, got: %s", output)
		}
		if strings.Contains(output, "---") {
			t.Errorf("--json should not contain separator, got: %s", output)
		}
	})
}
