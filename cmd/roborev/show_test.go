package main

// Tests for the show command

import (
	"encoding/json"
	"fmt"
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
		resolveRef   string
	}{
		{
			name: "numeric ref resolvable in repo treated as SHA not job ID",
			args: []string{"12345"},
			setupRepo: func(r *TestGitRepo) {
				r.Run("tag", "12345")
			},
			wantQueryHas: []string{"sha="},
			wantQueryNot: []string{"job_id="},
			resolveRef:   "12345",
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
			repo.CommitFile("file.txt", "content", "initial commit")
			if tt.setupRepo != nil {
				tt.setupRepo(repo)
			}

			getQuery := mockReviewDaemon(t, storage.Review{
				ID: 1, JobID: 42, Output: "LGTM", Agent: "test",
			})

			chdir(t, repo.Dir)
			_ = runShowCmd(t, tt.args...)

			q := getQuery()
			for _, s := range tt.wantQueryHas {
				if !strings.Contains(q, s) {
					t.Errorf("expected query to contain %q, got: %s", s, q)
				}
			}
			for _, s := range tt.wantQueryNot {
				if strings.Contains(q, s) {
					t.Errorf("expected query NOT to contain %q, got: %s", s, q)
				}
			}

			// Special check for resolved SHA
			if tt.resolveRef != "" {
				tagSHA := repo.Run("rev-parse", tt.resolveRef)
				if !strings.Contains(q, tagSHA[:7]) {
					t.Errorf("expected query to contain resolved SHA %s (from ref %s), got: %s", tagSHA[:7], tt.resolveRef, q)
				}
			}
		})
	}
}

func TestShowOutputFormat(t *testing.T) {
	tests := []struct {
		name       string
		argIsSHA   bool
		wantOutput string
		notOutput  string
	}{
		{
			name:       "job ID shows 'Review for job X (by agent)'",
			argIsSHA:   false,
			wantOutput: "Review for job 42 (by codex)",
			notOutput:  "(job 42, by",
		},
		{
			name:       "SHA shows 'Review for abc123 (job X, by agent)'",
			argIsSHA:   true,
			wantOutput: "Review for %s (job 42, by codex)", // %s will be replaced by shortSHA
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

			chdir(t, repo.Dir)

			var args []string
			if tt.argIsSHA {
				args = []string{commitSHA}
			} else {
				args = []string{"--job", "42"}
			}

			output := runShowCmd(t, args...)

			expected := tt.wantOutput
			if strings.Contains(expected, "%s") {
				expected = fmt.Sprintf(expected, shortSHA)
			}

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

func TestShowJSONOutput(t *testing.T) {
	t.Run("--json outputs valid JSON", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial commit")

		review := storage.Review{
			ID: 1, JobID: 42, Output: "LGTM", Agent: "test",
		}
		mockReviewDaemon(t, review)

		chdir(t, repo.Dir)

		cmd := showCmd()
		var buf strings.Builder
		cmd.SetOut(&buf)
		cmd.SetArgs([]string{"--job", "42", "--json"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		output := buf.String()
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

	t.Run("--json skips formatted header", func(t *testing.T) {
		repo := newTestGitRepo(t)
		repo.CommitFile("file.txt", "content", "initial commit")

		mockReviewDaemon(t, storage.Review{
			ID: 1, JobID: 42, Output: "LGTM", Agent: "test",
		})

		chdir(t, repo.Dir)

		cmd := showCmd()
		var buf strings.Builder
		cmd.SetOut(&buf)
		cmd.SetArgs([]string{"--job", "42", "--json"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		output := buf.String()
		if strings.Contains(output, "Review for") {
			t.Errorf("--json should not contain formatted header, got: %s", output)
		}
		if strings.Contains(output, "---") {
			t.Errorf("--json should not contain separator, got: %s", output)
		}
	})
}
