//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunRefineAgentErrorRetriesWithoutApplyingChanges(t *testing.T) {
	repoDir, headSHA := setupRefineRepo(t)

	md := NewMockDaemon(t, MockRefineHooks{})
	defer md.Close()

	md.State.reviews[headSHA] = &storage.Review{
		ID: 1, JobID: 7, Output: "**Bug found**: fail", Closed: false,
	}

	// Use 2 iterations so we can verify retry behavior
	agent.Register(&functionalMockAgent{nameVal: "test", reviewFunc: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
		return "", fmt.Errorf("test agent failure")
	}})
	defer agent.Register(agent.NewTestAgent())

	// Capture HEAD before running refine
	headBefore := gitRevParse(t, repoDir, "HEAD")

	ctx := defaultTestRunContext(repoDir)

	output := captureStdout(t, func() {
		// With 2 iterations and a failing agent, should exhaust iterations
		err := runRefine(ctx, refineOptions{agentName: "test", maxIterations: 2, quiet: true})
		require.NoError(t, err)
		
	})

	// Verify agent error message is printed (not shadowed by ResolveSHA)
	assert.Equal(t, false, !strings.Contains(output, "Agent error: test agent failure"), "unexpected condition")

	// Verify "Will retry in next iteration" message
	assert.Equal(t, false, !strings.Contains(output, "Will retry in next iteration"), "unexpected condition")

	// Verify no commit was created (HEAD unchanged)
	headAfter := gitRevParse(t, repoDir, "HEAD")
	assert.Equal(t, false, headBefore != headAfter, "unexpected condition")

	// Verify we attempted 2 iterations (both printed)
	assert.Equal(t, false, !strings.Contains(output, "=== Refinement iteration 1/2 ==="), "unexpected condition")
	assert.Equal(t, false, !strings.Contains(output, "=== Refinement iteration 2/2 ==="), "unexpected condition")
}

func handleMockRefineGetJobs(t *testing.T) func(w http.ResponseWriter, r *http.Request, s *mockRefineState) bool {
	return func(w http.ResponseWriter, r *http.Request, s *mockRefineState) bool {
		q := r.URL.Query()
		if idStr := q.Get("id"); idStr != "" {
			var jobID int64
			fmt.Sscanf(idStr, "%d", &jobID)
			s.mu.Lock()
			job, ok := s.jobs[jobID]
			if !ok {
				s.mu.Unlock()
				json.NewEncoder(w).Encode(map[string]any{"jobs": []storage.ReviewJob{}})
				return true
			}
			jobCopy := *job
			s.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"jobs": []storage.ReviewJob{jobCopy}})
			return true
		}
		if gitRef := q.Get("git_ref"); gitRef != "" {
			s.mu.Lock()
			var job *storage.ReviewJob
			for _, j := range s.jobs {
				if j.GitRef == gitRef {
					job = j
					break
				}
			}
			if job == nil {
				job = &storage.ReviewJob{
					ID:       s.nextJobID,
					GitRef:   gitRef,
					Agent:    "test",
					Status:   storage.JobStatusDone,
					RepoPath: q.Get("repo"),
				}
				s.jobs[job.ID] = job
				s.nextJobID++
			}
			if _, ok := s.reviews[gitRef]; !ok {
				s.reviews[gitRef] = &storage.Review{
					ID:     job.ID + 1000,
					JobID:  job.ID,
					Output: "**Bug**: fix failed",
				}
			}
			jobCopy := *job
			s.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"jobs": []storage.ReviewJob{jobCopy}})
			return true
		}
		return false // fall through to base handler
	}
}

func TestRefineLoopStaysOnFailedFixChain(t *testing.T) {
	repoDir, _ := setupRefineRepo(t)

	if err := os.WriteFile(filepath.Join(repoDir, "second.txt"), []byte("second"), 0644); err != nil {
		require.NoError(t, err)
	}
	execGit(t, repoDir, "add", "second.txt")
	execGit(t, repoDir, "commit", "-m", "second commit")

	commitList := strings.Fields(execGit(t, repoDir, "rev-list", "--reverse", "main..HEAD"))
	assert.Equal(t, false, len(commitList) < 2, "unexpected condition")
	oldestCommit := commitList[0]
	newestCommit := commitList[1]

	md := NewMockDaemon(t, MockRefineHooks{
		OnGetJobs: handleMockRefineGetJobs(t),
	})
	defer md.Close()

	md.State.nextJobID = 100
	md.State.reviews[oldestCommit] = &storage.Review{
		ID: 1, JobID: 1, Output: "**Bug**: old failure", Closed: false,
	}
	md.State.reviews[newestCommit] = &storage.Review{
		ID: 2, JobID: 2, Output: "**Bug**: new failure", Closed: false,
	}

	var changeCount int
	agent.Register(&functionalMockAgent{nameVal: "test", reviewFunc: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
		changeCount++
		change := fmt.Sprintf("fix %d", changeCount)
		if err := os.WriteFile(filepath.Join(repoPath, "fix.txt"), []byte(change), 0644); err != nil {
			return "", err
		}
		if output != nil {
			_, _ = output.Write([]byte(change))
		}
		return change, nil
	}})
	defer agent.Register(agent.NewTestAgent())

	ctx := defaultTestRunContext(repoDir)

	if err := runRefine(ctx, refineOptions{agentName: "test", maxIterations: 2, quiet: true}); err == nil {
		require.NoError(t, err)
	}

	for _, call := range md.State.respondCalled {
		if call.jobID == 2 {
			require.NoError(t, err, "expected to stay on failed fix chain; saw response for newer commit job 2")
		}
	}
}
