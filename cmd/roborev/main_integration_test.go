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
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/spf13/cobra"
)

func TestRunRefineAgentErrorRetriesWithoutApplyingChanges(t *testing.T) {
	repoDir, headSHA := setupRefineRepo(t)

	md := NewMockDaemon(t, MockRefineHooks{})
	defer md.Close()

	md.State.reviews[headSHA] = &storage.Review{
		ID: 1, JobID: 7, Output: "**Bug found**: fail", Closed: false,
	}

	testAgent := &functionalMockAgent{nameVal: "test", reviewFunc: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
		return "", fmt.Errorf("test agent failure")
	}}

	// Capture HEAD before running refine
	headBefore := gitRevParse(t, repoDir, "HEAD")

	ctx := defaultTestRunContext(repoDir)

	output := captureStdout(t, func() {
		// With 2 iterations and a failing agent, should exhaust iterations
		err := runRefine(&cobra.Command{}, ctx, refineOptions{
			agentName: "test",
			agentFactory: func(_ *config.Config, _ string, _ agent.ReasoningLevel, _ string) (agent.Agent, error) {
				return testAgent, nil
			},
			maxIterations: 2,
			quiet:         true,
		})
		if err == nil {
			t.Fatal("expected error after exhausting iterations, got nil")
		}
	})

	expectedStrings := []string{
		"Agent error: test agent failure",
		"Will retry in next iteration",
		"=== Refinement iteration 1/2 ===",
		"=== Refinement iteration 2/2 ===",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(output, expected) {
			t.Errorf("expected %q in output, got: %q", expected, output)
		}
	}

	// Verify no commit was created (HEAD unchanged)
	headAfter := gitRevParse(t, repoDir, "HEAD")
	if headBefore != headAfter {
		t.Errorf("expected HEAD to be unchanged after agent error, was %s now %s",
			headBefore, headAfter)
	}
}

func handleMockRefineGetJobs(t *testing.T) func(w http.ResponseWriter, r *http.Request, s *mockRefineState) bool {
	return func(w http.ResponseWriter, r *http.Request, s *mockRefineState) bool {
		q := r.URL.Query()

		if idStr := q.Get("id"); idStr != "" {
			return handleGetJobByID(w, idStr, s)
		}

		if gitRef := q.Get("git_ref"); gitRef != "" {
			return handleGetJobByGitRef(w, gitRef, q.Get("repo"), s)
		}

		return false
	}
}

func handleGetJobByID(w http.ResponseWriter, idStr string, s *mockRefineState) bool {
	var jobID int64
	fmt.Sscanf(idStr, "%d", &jobID)

	s.mu.Lock()
	jobs := []storage.ReviewJob{}
	if job, ok := s.jobs[jobID]; ok {
		jobs = append(jobs, *job)
	}
	s.mu.Unlock()

	json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
	return true
}

func handleGetJobByGitRef(w http.ResponseWriter, gitRef, repo string, s *mockRefineState) bool {
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
			RepoPath: repo,
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

func TestRefineLoopStaysOnFailedFixChain(t *testing.T) {
	repoDir, _ := setupRefineRepo(t)

	if err := os.WriteFile(filepath.Join(repoDir, "second.txt"), []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	execGit(t, repoDir, "add", "second.txt")
	execGit(t, repoDir, "commit", "-m", "second commit")

	commitList := strings.Fields(execGit(t, repoDir, "rev-list", "--reverse", "main..HEAD"))
	if len(commitList) < 2 {
		t.Fatalf("expected two commits on branch, got %d", len(commitList))
	}
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
	testAgent := &functionalMockAgent{nameVal: "test", reviewFunc: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
		changeCount++
		change := fmt.Sprintf("fix %d", changeCount)
		if err := os.WriteFile(filepath.Join(repoPath, "fix.txt"), []byte(change), 0644); err != nil {
			return "", err
		}
		if output != nil {
			_, _ = output.Write([]byte(change))
		}
		return change, nil
	}}

	ctx := defaultTestRunContext(repoDir)

	if err := runRefine(&cobra.Command{}, ctx, refineOptions{
		agentName: "test",
		agentFactory: func(_ *config.Config, _ string, _ agent.ReasoningLevel, _ string) (agent.Agent, error) {
			return testAgent, nil
		},
		maxIterations: 2,
		quiet:         true,
	}); err == nil {
		t.Fatal("expected error from reaching max iterations")
	}

	for _, call := range md.State.respondCalled {
		if call.jobID == 2 {
			t.Fatalf("expected to stay on failed fix chain; saw response for newer commit job 2")
		}
	}
}
