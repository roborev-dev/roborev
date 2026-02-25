package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/storage"
)

func TestBuildGenericFixPrompt(t *testing.T) {
	analysisOutput := `## Issues Found
- Long function in main.go:50
- Missing error handling`

	prompt := buildGenericFixPrompt(analysisOutput)

	// Should include the analysis output
	if !strings.Contains(prompt, "Issues Found") {
		t.Error("prompt should include analysis output")
	}
	if !strings.Contains(prompt, "Long function") {
		t.Error("prompt should include specific findings")
	}

	// Should have fix instructions
	if !strings.Contains(prompt, "apply the suggested changes") {
		t.Error("prompt should include fix instructions")
	}

	// Should request a commit
	if !strings.Contains(prompt, "git commit") {
		t.Error("prompt should request a commit")
	}
}

func TestBuildGenericCommitPrompt(t *testing.T) {
	prompt := buildGenericCommitPrompt()

	// Should have commit instructions
	if !strings.Contains(prompt, "git commit") {
		t.Error("prompt should mention git commit")
	}
	if !strings.Contains(prompt, "descriptive") {
		t.Error("prompt should request a descriptive message")
	}
}

func TestFetchJob(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		jobs       []storage.ReviewJob
		wantErr    bool
		wantErrMsg string
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			jobs: []storage.ReviewJob{{
				ID:     42,
				Status: storage.JobStatusDone,
				Agent:  "test",
			}},
		},
		{
			name:       "not found",
			statusCode: http.StatusOK,
			jobs:       []storage.ReviewJob{},
			wantErr:    true,
			wantErrMsg: "not found",
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
			wantErrMsg: "server error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					writeJSON(w, map[string]any{"jobs": tt.jobs})
				}
			}))
			defer ts.Close()

			job, err := fetchJob(context.Background(), ts.URL, 42)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				} else if !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrMsg)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if job.ID != 42 {
				t.Errorf("job.ID = %d, want 42", job.ID)
			}
		})
	}
}

func TestFetchReview(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		review     *storage.Review
		wantErr    bool
		wantErrMsg string
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			review: &storage.Review{
				JobID:  42,
				Output: "Analysis output here",
			},
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
			wantErrMsg: "server error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/review" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.URL.Query().Get("job_id") != "42" {
					t.Errorf("unexpected job_id: %s", r.URL.Query().Get("job_id"))
				}

				w.WriteHeader(tt.statusCode)
				if tt.review != nil {
					writeJSON(w, tt.review)
				}
			}))
			defer ts.Close()

			review, err := fetchReview(context.Background(), ts.URL, 42)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				} else if !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrMsg)
				}
				return
			}

			if err != nil {
				t.Fatalf("fetchReview: %v", err)
			}
			if review.JobID != tt.review.JobID {
				t.Errorf("review.JobID = %d, want %d", review.JobID, tt.review.JobID)
			}
			if review.Output != tt.review.Output {
				t.Errorf("review.Output = %q, want %q", review.Output, tt.review.Output)
			}
		})
	}
}

func TestAddJobResponse(t *testing.T) {
	var gotJobID int64
	var gotContent string

	var gotCommenter string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/comment" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request body: %v", err)
			return
		}
		gotJobID = int64(req["job_id"].(float64))
		gotContent = req["comment"].(string)
		gotCommenter = req["commenter"].(string)

		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()

	err := addJobResponse(ts.URL, 123, "roborev-fix", "Fix applied")
	if err != nil {
		t.Fatalf("addJobResponse: %v", err)
	}

	if gotJobID != 123 {
		t.Errorf("job_id = %d, want 123", gotJobID)
	}
	if gotContent != "Fix applied" {
		t.Errorf("comment = %q, want %q", gotContent, "Fix applied")
	}
	if gotCommenter != "roborev-fix" {
		t.Errorf("commenter = %q, want %q", gotCommenter, "roborev-fix")
	}
}

func TestFixSingleJob(t *testing.T) {
	repo := createTestRepo(t, map[string]string{
		"main.go": "package main\n",
	})

	ts, _ := newMockServer(t, MockServerOpts{
		ReviewOutput: "## Issues\n- Found minor issue",
		OnJobs: func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{{
					ID:     99,
					Status: storage.JobStatusDone,
					Agent:  "test",
				}},
			})
		},
	})
	patchServerAddr(t, ts.URL)

	cmd, output := newTestCmd(t)

	opts := fixOptions{
		agentName: "test",
		reasoning: "fast",
	}

	err := fixSingleJob(cmd, repo.Dir, 99, opts)
	if err != nil {
		t.Fatalf("fixSingleJob: %v", err)
	}

	// Verify output contains expected content
	outputStr := output.String()
	if !strings.Contains(outputStr, "Issues") {
		t.Error("output should show analysis findings")
	}
	if !strings.Contains(outputStr, "marked as addressed") {
		t.Error("output should confirm job addressed")
	}
}

func TestFixJobNotComplete(t *testing.T) {
	ts, _ := newMockServer(t, MockServerOpts{
		OnJobs: func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"jobs": []storage.ReviewJob{{
					ID:     99,
					Status: storage.JobStatusRunning, // Not complete
					Agent:  "test",
				}},
			})
		},
	})
	patchServerAddr(t, ts.URL)

	cmd, _ := newTestCmd(t)

	err := fixSingleJob(cmd, t.TempDir(), 99, fixOptions{agentName: "test"})

	if err == nil {
		t.Error("expected error for incomplete job")
	}
	if !strings.Contains(err.Error(), "not complete") {
		t.Errorf("error %q should mention 'not complete'", err.Error())
	}
}

func TestFixCmdFlagValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "--branch without --unaddressed",
			args:    []string{"--branch", "main"},
			wantErr: "--branch requires --unaddressed",
		},
		{
			name:    "--all-branches with positional args",
			args:    []string{"--all-branches", "123"},
			wantErr: "--unaddressed cannot be used with positional job IDs",
		},
		{
			name:    "--unaddressed with positional args",
			args:    []string{"--unaddressed", "123"},
			wantErr: "--unaddressed cannot be used with positional job IDs",
		},
		{
			name:    "--newest-first without --unaddressed",
			args:    []string{"--newest-first", "123"},
			wantErr: "--newest-first requires --unaddressed",
		},
		{
			name:    "--all-branches with --branch (no explicit --unaddressed)",
			args:    []string{"--all-branches", "--branch", "main"},
			wantErr: "--all-branches and --branch are mutually exclusive",
		},
		{
			name:    "--batch with --unaddressed",
			args:    []string{"--batch", "--unaddressed"},
			wantErr: "--batch and --unaddressed are mutually exclusive",
		},
		{
			name:    "--batch with explicit IDs and --branch",
			args:    []string{"--batch", "--branch", "main", "123"},
			wantErr: "cannot be used with explicit job IDs",
		},
		{
			name:    "--batch with explicit IDs and --all-branches",
			args:    []string{"--batch", "--all-branches", "123"},
			wantErr: "cannot be used with explicit job IDs",
		},
		{
			name:    "--batch with explicit IDs and --newest-first",
			args:    []string{"--batch", "--newest-first", "123"},
			wantErr: "cannot be used with explicit job IDs",
		},
		{
			name:    "--list with positional args",
			args:    []string{"--list", "123"},
			wantErr: "--list cannot be used with positional job IDs",
		},
		{
			name:    "--list with --batch",
			args:    []string{"--list", "--batch"},
			wantErr: "--list and --batch are mutually exclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := fixCmd()
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestFixNoArgsDefaultsToUnaddressed(t *testing.T) {
	// Running fix with no args should not produce a validation error —
	// it should enter the unaddressed path (which will fail at daemon
	// connection, not at argument validation).
	//
	// Use a mock daemon so ensureDaemon doesn't try to spawn a real
	// daemon subprocess (which hangs on CI).
	daemonFromHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return empty for all queries — we only care about argument routing
		json.NewEncoder(w).Encode(map[string]any{
			"jobs":     []any{},
			"has_more": false,
		})
	}))

	cmd := fixCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	// Should NOT be a validation/args error; any other error (e.g. daemon
	// not running) is acceptable.
	if err != nil && strings.Contains(err.Error(), "requires at least") {
		t.Errorf("no-args should default to --unaddressed, got validation error: %v", err)
	}
}

func TestFixAllBranchesImpliesUnaddressed(t *testing.T) {
	// --all-branches alone should imply --unaddressed and pass
	// validation, routing through unaddressed discovery.
	daemonFromHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"jobs":     []any{},
			"has_more": false,
		})
	}))

	cmd := fixCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"--all-branches"})
	err := cmd.Execute()
	if err != nil {
		t.Errorf("--all-branches should not fail validation: %v", err)
	}
}

func TestRunFixUnaddressed(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	t.Run("no unaddressed jobs", func(t *testing.T) {
		_ = newMockDaemonBuilder(t).
			WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get("status") != "done" {
					t.Errorf("expected status=done, got %q", q.Get("status"))
				}
				if q.Get("addressed") != "false" {
					t.Errorf("expected addressed=false, got %q", q.Get("addressed"))
				}
				writeJSON(w, map[string]any{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
			}).
			Build()

		out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixUnaddressed(cmd, "", false, fixOptions{agentName: "test"})
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "No unaddressed jobs found") {
			t.Errorf("expected 'No unaddressed jobs found' message, got %q", out)
		}
	})

	t.Run("finds and processes unaddressed jobs", func(t *testing.T) {
		var reviewCalls, addressCalls atomic.Int32
		var unaddressedCalls atomic.Int32

		_ = newMockDaemonBuilder(t).
			WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get("addressed") == "false" && q.Get("limit") == "0" {
					if unaddressedCalls.Add(1) == 1 {
						writeJSON(w, map[string]any{
							"jobs": []storage.ReviewJob{
								{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
								{ID: 20, Status: storage.JobStatusDone, Agent: "test"},
							},
							"has_more": false,
						})
					} else {
						writeJSON(w, map[string]any{
							"jobs":     []storage.ReviewJob{},
							"has_more": false,
						})
					}
				} else {
					writeJSON(w, map[string]any{
						"jobs": []storage.ReviewJob{
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				}
			}).
			WithHandler("/api/review", func(w http.ResponseWriter, r *http.Request) {
				reviewCalls.Add(1)
				writeJSON(w, storage.Review{Output: "findings"})
			}).
			WithHandler("/api/review/address", func(w http.ResponseWriter, r *http.Request) {
				addressCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}).
			WithHandler("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}).
			Build()

		out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixUnaddressed(cmd, "", false, fixOptions{agentName: "test", reasoning: "fast"})
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "Found 2 unaddressed job(s)") {
			t.Errorf("expected count message, got %q", out)
		}
		if rc := reviewCalls.Load(); rc != 2 {
			t.Errorf("expected 2 review fetches, got %d", rc)
		}
		if ac := addressCalls.Load(); ac != 2 {
			t.Errorf("expected 2 address calls, got %d", ac)
		}
	})

	t.Run("passes branch filter to API", func(t *testing.T) {
		var gotBranch string
		_ = newMockDaemonBuilder(t).
			WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("addressed") == "false" {
					gotBranch = r.URL.Query().Get("branch")
				}
				writeJSON(w, map[string]any{
					"jobs":     []storage.ReviewJob{},
					"has_more": false,
				})
			}).
			Build()

		_, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixUnaddressed(cmd, "feature-branch", false, fixOptions{agentName: "test"})
		})
		if err != nil {
			t.Fatalf("runFixUnaddressed returned unexpected error: %v", err)
		}
		if gotBranch != "feature-branch" {
			t.Errorf("expected branch=feature-branch, got %q", gotBranch)
		}
	})

	t.Run("server error", func(t *testing.T) {
		_ = newMockDaemonBuilder(t).
			WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("db error"))
			}).
			Build()

		_, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixUnaddressed(cmd, "", false, fixOptions{agentName: "test"})
		})
		if err == nil {
			t.Fatal("expected error on server failure")
		}
		if !strings.Contains(err.Error(), "server error") {
			t.Errorf("error %q should mention server error", err.Error())
		}
	})
}
func TestRunFixUnaddressedOrdering(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	makeBuilder := func() (*MockDaemonBuilder, *atomic.Int32) {
		var unaddressedCalls atomic.Int32
		b := newMockDaemonBuilder(t).
			WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get("addressed") == "false" {
					if unaddressedCalls.Add(1) == 1 {
						// Return newest first (as the API does)
						writeJSON(w, map[string]any{
							"jobs": []storage.ReviewJob{
								{ID: 30, Status: storage.JobStatusDone, Agent: "test"},
								{ID: 20, Status: storage.JobStatusDone, Agent: "test"},
								{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
							},
							"has_more": false,
						})
					} else {
						writeJSON(w, map[string]any{
							"jobs":     []storage.ReviewJob{},
							"has_more": false,
						})
					}
				} else {
					writeJSON(w, map[string]any{
						"jobs": []storage.ReviewJob{
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				}
			}).
			WithHandler("/api/review", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, storage.Review{Output: "findings"})
			}).
			WithHandler("/api/comment", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
			}).
			WithHandler("/api/review/address", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}).
			WithHandler("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
		return b, &unaddressedCalls
	}

	t.Run("oldest first by default", func(t *testing.T) {
		b, _ := makeBuilder()
		b.Build()

		out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixUnaddressed(cmd, "", false, fixOptions{agentName: "test", reasoning: "fast"})
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "[10 20 30]") {
			t.Errorf("expected oldest-first order [10 20 30], got %q", out)
		}
	})

	t.Run("newest first with flag", func(t *testing.T) {
		b, _ := makeBuilder()
		b.Build()

		out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixUnaddressed(cmd, "", true, fixOptions{agentName: "test", reasoning: "fast"})
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "[30 20 10]") {
			t.Errorf("expected newest-first order [30 20 10], got %q", out)
		}
	})
}
func TestRunFixUnaddressedRequery(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	var queryCount atomic.Int32
	_ = newMockDaemonBuilder(t).
		WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("addressed") == "false" && q.Get("limit") == "0" {
				n := queryCount.Add(1)
				switch n {
				case 1:
					// First query: return batch 1
					writeJSON(w, map[string]any{
						"jobs": []storage.ReviewJob{
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				case 2:
					// Second query: new job appeared
					writeJSON(w, map[string]any{
						"jobs": []storage.ReviewJob{
							{ID: 20, Status: storage.JobStatusDone, Agent: "test"},
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				default:
					// Third query: no new jobs
					writeJSON(w, map[string]any{
						"jobs":     []storage.ReviewJob{},
						"has_more": false,
					})
				}
			} else {
				// Individual job fetch
				writeJSON(w, map[string]any{
					"jobs": []storage.ReviewJob{
						{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
					},
					"has_more": false,
				})
			}
		}).
		WithHandler("/api/review", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, storage.Review{Output: "findings"})
		}).
		WithHandler("/api/comment", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
		}).
		WithHandler("/api/review/address", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).
		WithHandler("/api/enqueue", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).
		Build()

	out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
		return runFixUnaddressed(cmd, "", false, fixOptions{agentName: "test", reasoning: "fast"})
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "Found 1 unaddressed job(s)") {
		t.Errorf("expected first batch message, got %q", out)
	}
	if !strings.Contains(out, "Found 1 new unaddressed job(s)") {
		t.Errorf("expected second batch message, got %q", out)
	}
	if int(queryCount.Load()) != 3 {
		t.Errorf("expected 3 queries, got %d", queryCount.Load())
	}
}

func TestFixJobDirectUnbornHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	t.Run("agent creates first commit", func(t *testing.T) {
		// Create a fresh git repo with no commits (unborn HEAD)
		dir := t.TempDir()
		cmd := exec.Command("git", "init")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		for _, args := range [][]string{
			{"config", "user.email", "test@test.com"},
			{"config", "user.name", "Test"},
		} {
			c := exec.Command("git", args...)
			c.Dir = dir
			if err := c.Run(); err != nil {
				t.Fatalf("git %v: %v", args, err)
			}
		}

		ag := &agent.FakeAgent{
			NameStr: "test",
			ReviewFn: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
				// Simulate agent creating the first commit
				if err := os.WriteFile(filepath.Join(repoPath, "fix.txt"), []byte("fixed"), 0644); err != nil {
					return "", fmt.Errorf("write file: %w", err)
				}
				c := exec.Command("git", "add", ".")
				c.Dir = repoPath
				if err := c.Run(); err != nil {
					return "", fmt.Errorf("git add: %w", err)
				}
				c = exec.Command("git", "commit", "-m", "first commit")
				c.Dir = repoPath
				if err := c.Run(); err != nil {
					return "", fmt.Errorf("git commit: %w", err)
				}
				return "applied fix", nil
			},
		}

		result, err := fixJobDirect(context.Background(), fixJobParams{
			RepoRoot: dir,
			Agent:    ag,
		}, "fix things")
		if err != nil {
			t.Fatalf("fixJobDirect: %v", err)
		}
		if !result.CommitCreated {
			t.Error("expected CommitCreated=true")
		}
		if result.NoChanges {
			t.Error("expected NoChanges=false")
		}
		if result.NewCommitSHA == "" {
			t.Error("expected NewCommitSHA to be set")
		}
	})

	t.Run("agent makes no changes on unborn head", func(t *testing.T) {
		dir := t.TempDir()
		cmd := exec.Command("git", "init")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}

		ag := &agent.FakeAgent{
			NameStr: "test",
			ReviewFn: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
				return "nothing to do", nil
			},
		}

		result, err := fixJobDirect(context.Background(), fixJobParams{
			RepoRoot: dir,
			Agent:    ag,
		}, "fix things")
		if err != nil {
			t.Fatalf("fixJobDirect: %v", err)
		}
		if result.CommitCreated {
			t.Error("expected CommitCreated=false")
		}
		if !result.NoChanges {
			t.Error("expected NoChanges=true")
		}
	})
}

func TestBuildBatchFixPrompt(t *testing.T) {
	entries := []batchEntry{
		{
			jobID:  123,
			job:    &storage.ReviewJob{GitRef: "abc123def456"},
			review: &storage.Review{Output: "Found bug in foo.go"},
		},
		{
			jobID:  456,
			job:    &storage.ReviewJob{GitRef: "deadbeef1234"},
			review: &storage.Review{Output: "Missing error check in bar.go"},
		},
	}

	prompt := buildBatchFixPrompt(entries)

	// Header
	if !strings.Contains(prompt, "# Batch Fix Request") {
		t.Error("prompt should have batch header")
	}
	if !strings.Contains(prompt, "Address all findings across all reviews in a single pass") {
		t.Error("prompt should instruct single-pass fix")
	}

	// Per-review sections with numbered headers
	if !strings.Contains(prompt, "## Review 1 (Job 123 — abc123d)") {
		t.Errorf("prompt missing review 1 header, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Found bug in foo.go") {
		t.Error("prompt should include first review output")
	}
	if !strings.Contains(prompt, "## Review 2 (Job 456 — deadbee)") {
		t.Errorf("prompt missing review 2 header, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Missing error check in bar.go") {
		t.Error("prompt should include second review output")
	}

	// Instructions footer
	if !strings.Contains(prompt, "## Instructions") {
		t.Error("prompt should have instructions section")
	}
	if !strings.Contains(prompt, "git commit") {
		t.Error("prompt should request a commit")
	}
}

func TestBuildBatchFixPromptSingleEntry(t *testing.T) {
	entries := []batchEntry{
		{
			jobID:  7,
			job:    &storage.ReviewJob{GitRef: "aaa"},
			review: &storage.Review{Output: "one issue"},
		},
	}

	prompt := buildBatchFixPrompt(entries)
	if !strings.Contains(prompt, "## Review 1 (Job 7") {
		t.Error("single-entry batch should still have numbered header")
	}
}

func TestSplitIntoBatches(t *testing.T) {
	makeEntry := func(id int64, outputSize int) batchEntry {
		return batchEntry{
			jobID:  id,
			job:    &storage.ReviewJob{GitRef: fmt.Sprintf("sha%d", id)},
			review: &storage.Review{Output: strings.Repeat("x", outputSize)},
		}
	}

	t.Run("all fit in one batch", func(t *testing.T) {
		entries := []batchEntry{
			makeEntry(1, 100),
			makeEntry(2, 100),
			makeEntry(3, 100),
		}
		batches := splitIntoBatches(entries, 100000)
		if len(batches) != 1 {
			t.Errorf("expected 1 batch, got %d", len(batches))
		}
		if len(batches[0]) != 3 {
			t.Errorf("expected 3 entries in batch, got %d", len(batches[0]))
		}
	})

	t.Run("splits when exceeding limit", func(t *testing.T) {
		entries := []batchEntry{
			makeEntry(1, 500),
			makeEntry(2, 500),
			makeEntry(3, 500),
		}
		// Set limit small enough that not all fit (overhead ~300 bytes + entry ~530 each)
		maxSize := 1000
		batches := splitIntoBatches(entries, maxSize)
		if len(batches) < 2 {
			t.Errorf("expected at least 2 batches, got %d", len(batches))
		}
		// All entries should be present across batches
		total := 0
		for _, b := range batches {
			total += len(b)
		}
		if total != 3 {
			t.Errorf("expected 3 total entries, got %d", total)
		}
	})

	t.Run("oversized single review gets own batch", func(t *testing.T) {
		entries := []batchEntry{
			makeEntry(1, 100),
			makeEntry(2, 5000), // oversized
			makeEntry(3, 100),
		}
		batches := splitIntoBatches(entries, 1000)
		if len(batches) < 2 {
			t.Errorf("expected at least 2 batches, got %d", len(batches))
		}
		// The oversized entry should be alone in its batch
		found := false
		for _, b := range batches {
			for _, e := range b {
				if e.jobID == 2 && len(b) == 1 {
					found = true
				}
			}
		}
		if !found {
			t.Error("oversized entry (job 2) should be alone in its batch")
		}
	})

	t.Run("empty input", func(t *testing.T) {
		batches := splitIntoBatches(nil, 100000)
		if len(batches) != 0 {
			t.Errorf("expected 0 batches for empty input, got %d", len(batches))
		}
	})

	t.Run("built prompt respects size estimate", func(t *testing.T) {
		// Verify that splitIntoBatches size accounting matches buildBatchFixPrompt output.
		entries := []batchEntry{
			makeEntry(1, 200),
			makeEntry(2, 200),
			makeEntry(3, 200),
			makeEntry(4, 200),
			makeEntry(5, 200),
		}
		maxSize := 1000
		batches := splitIntoBatches(entries, maxSize)
		for i, batch := range batches {
			prompt := buildBatchFixPrompt(batch)
			// Single-entry batches that are inherently oversized are allowed to exceed.
			if len(batch) > 1 && len(prompt) > maxSize {
				t.Errorf("batch %d prompt size %d exceeds maxSize %d", i, len(prompt), maxSize)
			}
		}
	})

	t.Run("preserves order", func(t *testing.T) {
		entries := []batchEntry{
			makeEntry(10, 100),
			makeEntry(20, 100),
			makeEntry(30, 100),
		}
		batches := splitIntoBatches(entries, 100000)
		if len(batches) != 1 {
			t.Fatalf("expected 1 batch, got %d", len(batches))
		}
		for i, want := range []int64{10, 20, 30} {
			if batches[0][i].jobID != want {
				t.Errorf("batch[0][%d].jobID = %d, want %d", i, batches[0][i].jobID, want)
			}
		}
	})
}

func TestFormatJobIDs(t *testing.T) {
	tests := []struct {
		ids  []int64
		want string
	}{
		{[]int64{1}, "1"},
		{[]int64{1, 2, 3}, "1, 2, 3"},
		{[]int64{100, 200}, "100, 200"},
	}
	for _, tt := range tests {
		got := formatJobIDs(tt.ids)
		if got != tt.want {
			t.Errorf("formatJobIDs(%v) = %q, want %q", tt.ids, got, tt.want)
		}
	}
}

func TestEnqueueIfNeededSkipsWhenJobExists(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})
	sha := "abc123def456"

	var enqueueCalls atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/jobs":
			// Return an existing job — hook already fired
			json.NewEncoder(w).Encode(map[string]any{
				"jobs": []map[string]any{{"id": 42}},
			})
		case "/api/enqueue":
			enqueueCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": 99})
		}
	}))
	defer ts.Close()

	err := enqueueIfNeeded(ts.URL, repo.Dir, sha)
	if err != nil {
		t.Fatalf("enqueueIfNeeded: %v", err)
	}
	if enqueueCalls.Load() != 0 {
		t.Error("should not enqueue when job already exists on first check")
	}
}

func TestRunFixList(t *testing.T) {
	repo := createTestRepo(t, map[string]string{"f.txt": "x"})

	t.Run("lists unaddressed jobs with details", func(t *testing.T) {
		finishedAt := time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC)
		verdict := "FAIL"

		_ = newMockDaemonBuilder(t).
			WithJobs([]storage.ReviewJob{{
				ID:            42,
				GitRef:        "abc123def456",
				Branch:        "feature-branch",
				CommitSubject: "Fix the widget",
				Agent:         "claude-code",
				Model:         "claude-3-opus",
				Status:        storage.JobStatusDone,
				FinishedAt:    &finishedAt,
				Verdict:       &verdict,
			}}).
			WithReview(42, "Found 3 issues:\n- Missing error handling\n- Unused variable").
			Build()
			// serverAddr is patched by daemonFromHandler called inside Build()

		out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixList(cmd, "", false)
		})
		if err != nil {
			t.Fatalf("runFixList: %v", err)
		}

		// Check header
		if !strings.Contains(out, "Found 1 unaddressed fix(es):") {
			t.Errorf("expected header message, got:\n%s", out)
		}

		// Check job details are displayed
		if !strings.Contains(out, "Job #42") {
			t.Errorf("expected job ID, got:\n%s", out)
		}
		if !strings.Contains(out, "Git Ref:  abc123d") {
			t.Errorf("expected git ref, got:\n%s", out)
		}
		if !strings.Contains(out, "Branch:   feature-branch") {
			t.Errorf("expected branch, got:\n%s", out)
		}
		if !strings.Contains(out, "Subject:  Fix the widget") {
			t.Errorf("expected subject, got:\n%s", out)
		}
		if !strings.Contains(out, "Agent:    claude-code") {
			t.Errorf("expected agent, got:\n%s", out)
		}
		if !strings.Contains(out, "Model:    claude-3-opus") {
			t.Errorf("expected model, got:\n%s", out)
		}
		if !strings.Contains(out, "Verdict:  FAIL") {
			t.Errorf("expected verdict, got:\n%s", out)
		}
		if !strings.Contains(out, "Summary:  Found 3 issues:") {
			t.Errorf("expected summary, got:\n%s", out)
		}

		// Check usage hints
		if !strings.Contains(out, "roborev fix <job_id>") {
			t.Errorf("expected usage hint, got:\n%s", out)
		}
		if !strings.Contains(out, "roborev fix --unaddressed") {
			t.Errorf("expected unaddressed hint, got:\n%s", out)
		}
	})

	t.Run("no unaddressed jobs", func(t *testing.T) {
		_ = newMockDaemonBuilder(t).
			WithJobs([]storage.ReviewJob{}).
			Build()

		out, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixList(cmd, "", false)
		})
		if err != nil {
			t.Fatalf("runFixList: %v", err)
		}

		if !strings.Contains(out, "No unaddressed jobs found") {
			t.Errorf("expected no jobs message, got:\n%s", out)
		}
	})

	t.Run("respects newest-first flag", func(t *testing.T) {
		var gotIDs []int64
		_ = newMockDaemonBuilder(t).
			WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get("addressed") == "false" && q.Get("limit") == "0" {
					// API returns newest first
					writeJSON(w, map[string]any{
						"jobs": []storage.ReviewJob{
							{ID: 30, Status: storage.JobStatusDone, Agent: "test"},
							{ID: 20, Status: storage.JobStatusDone, Agent: "test"},
							{ID: 10, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				} else if q.Get("id") != "" {
					var id int64
					_, _ = fmt.Sscanf(q.Get("id"), "%d", &id)
					gotIDs = append(gotIDs, id)
					writeJSON(w, map[string]any{
						"jobs": []storage.ReviewJob{
							{ID: id, Status: storage.JobStatusDone, Agent: "test"},
						},
						"has_more": false,
					})
				}
			}).
			WithReview(30, "findings").
			WithReview(20, "findings").
			WithReview(10, "findings").
			Build()

		gotIDs = nil
		_, err := runWithOutput(t, repo.Dir, func(cmd *cobra.Command) error {
			return runFixList(cmd, "", true)
		})
		if err != nil {
			t.Fatalf("runFixList: %v", err)
		}

		if len(gotIDs) != 3 {
			t.Fatalf("expected 3 job fetches, got %d", len(gotIDs))
		}
		if gotIDs[0] != 30 || gotIDs[1] != 20 || gotIDs[2] != 10 {
			t.Errorf("expected newest-first order [30, 20, 10], got %v", gotIDs)
		}
	})
}
func TestTruncateString(t *testing.T) {
	tests := []struct {
		s      string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 8, "hello..."},
		{"hello world", 5, "he..."},
		{"hi", 3, "hi"},
		{"hello", 3, "hel"},
		{"", 10, ""},
		// Edge cases for maxLen <= 0
		{"hello", 0, ""},
		{"hello", -1, ""},
		// Unicode handling: ensure multi-byte characters aren't split
		{"こんにちは世界", 5, "こん..."},      // Japanese: maxLen=5, output is 2 chars + "..." = 5 runes
		{"こんにちは", 10, "こんにちは"},       // Japanese: fits within limit
		{"Hello 世界!", 8, "Hello..."}, // Mixed ASCII and Unicode
		{"🎉🎊🎁🎄🎅", 3, "🎉🎊🎁"},          // Emoji: exactly 3 runes
		{"🎉🎊🎁🎄🎅", 4, "🎉..."},         // Emoji: truncate with ellipsis
	}

	for _, tt := range tests {
		got := truncateString(tt.s, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncateString(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
		}
	}
}

// setupWorktree creates a main repo with a commit and a worktree, returning
// the main repo and the worktree directory path.
func setupWorktree(t *testing.T) (mainRepo *TestGitRepo, worktreeDir string) {
	t.Helper()
	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial")

	wtDir := t.TempDir()
	os.Remove(wtDir)
	repo.Run("worktree", "add", "-b", "wt-branch", wtDir)
	return repo, wtDir
}

// setupWorktreeMockDaemon sets up a mock daemon that captures the repo query
// param from /api/jobs requests, returning empty results.
func setupWorktreeMockDaemon(t *testing.T) (receivedRepo *string) {
	t.Helper()
	var repo string
	_ = newMockDaemonBuilder(t).
		WithHandler("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
			repo = r.URL.Query().Get("repo")
			writeJSON(w, map[string]any{
				"jobs":     []storage.ReviewJob{},
				"has_more": false,
			})
		}).
		Build()
	return &repo
}

func TestFixWorktreeRepoResolution(t *testing.T) {
	t.Run("runFixList sends main repo path", func(t *testing.T) {
		receivedRepo := setupWorktreeMockDaemon(t)
		repo, worktreeDir := setupWorktree(t)
		chdir(t, worktreeDir)

		cmd := &cobra.Command{}
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := runFixList(cmd, "", false); err != nil {
			t.Fatalf("runFixList: %v", err)
		}

		if *receivedRepo == "" {
			t.Fatal("expected repo param to be sent")
		}
		if *receivedRepo != repo.Dir {
			t.Errorf("expected main repo path %q, got %q", repo.Dir, *receivedRepo)
		}
	})

	t.Run("runFixUnaddressed sends main repo path", func(t *testing.T) {
		receivedRepo := setupWorktreeMockDaemon(t)
		repo, worktreeDir := setupWorktree(t)
		chdir(t, worktreeDir)

		cmd := &cobra.Command{}
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		opts := fixOptions{quiet: true}
		if err := runFixUnaddressed(cmd, "", false, opts); err != nil {
			t.Fatalf("runFixUnaddressed: %v", err)
		}

		if *receivedRepo == "" {
			t.Fatal("expected repo param to be sent")
		}
		if *receivedRepo != repo.Dir {
			t.Errorf("expected main repo path %q, got %q", repo.Dir, *receivedRepo)
		}
	})

	t.Run("runFixBatch sends main repo path", func(t *testing.T) {
		receivedRepo := setupWorktreeMockDaemon(t)
		repo, worktreeDir := setupWorktree(t)
		chdir(t, worktreeDir)

		cmd := &cobra.Command{}
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		opts := fixOptions{quiet: true}
		// nil jobIDs triggers discovery via queryUnaddressedJobs
		if err := runFixBatch(cmd, nil, "", false, opts); err != nil {
			t.Fatalf("runFixBatch: %v", err)
		}

		if *receivedRepo == "" {
			t.Fatal("expected repo param to be sent")
		}
		if *receivedRepo != repo.Dir {
			t.Errorf("expected main repo path %q, got %q", repo.Dir, *receivedRepo)
		}
	})
}
