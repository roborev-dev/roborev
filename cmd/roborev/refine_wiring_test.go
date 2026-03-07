package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/spf13/cobra"
)

func TestRunRefineAgentFactoryWiring(t *testing.T) {
	origIsTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return true }
	defer func() { isTerminal = origIsTerminal }()

	t.Run("explicit flags", func(t *testing.T) {
		t.Setenv("ROBOREV_DATA_DIR", t.TempDir())

		mockAgent := &functionalMockAgent{
			nameVal: "test",
			reviewFunc: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
				return "fixed it", nil
			},
		}
		repoDir, headSHA := setupRefineRepo(t)
		ctx := newFastRunContext(repoDir)

		md := NewMockDaemon(t, MockRefineHooks{})
		defer md.Close()

		// Simulate one failed review for HEAD
		md.State.reviews[headSHA] = &storage.Review{
			ID: 1, JobID: 7, Output: "**Bug found**: fail", Closed: false,
		}

		var capturedAgent string
		var capturedReasoning agent.ReasoningLevel
		var capturedBackup string

		factory := func(_ *config.Config, resolvedAgent string, reasoningLevel agent.ReasoningLevel, backupAgent string) (agent.Agent, error) {
			capturedAgent = resolvedAgent
			capturedReasoning = reasoningLevel
			capturedBackup = backupAgent
			return mockAgent, nil
		}

		opts := refineOptions{
			agentName:     "my-agent",
			model:         "my-model",
			reasoning:     "fast",
			agentFactory:  factory,
			quiet:         true,
			maxIterations: 1,
		}

		err := runRefine(&cobra.Command{}, ctx, opts)
		if err == nil || !strings.Contains(err.Error(), "max iterations (1) reached") {
			t.Fatalf("expected max iterations error, got: %v", err)
		}

		if capturedAgent != "my-agent" {
			t.Errorf("expected agent 'my-agent', got %q", capturedAgent)
		}
		if capturedBackup != "" {
			t.Errorf("expected empty backup agent, got %q", capturedBackup)
		}
		if capturedReasoning != agent.ReasoningLevel("fast") {
			t.Errorf("expected reasoning 'fast', got %q", capturedReasoning)
		}
		if len(mockAgent.withModelCalls) == 0 || mockAgent.withModelCalls[len(mockAgent.withModelCalls)-1] != "my-model" {
			t.Errorf("expected WithModel('my-model') to be called, got calls: %v", mockAgent.withModelCalls)
		}
	})

	t.Run("config fallback", func(t *testing.T) {
		t.Setenv("ROBOREV_DATA_DIR", t.TempDir())

		mockAgent := &functionalMockAgent{
			nameVal: "test",
			reviewFunc: func(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
				return "fixed it", nil
			},
		}

		repoDir, headSHA := setupRefineRepo(t)
		ctx := newFastRunContext(repoDir)

		md := NewMockDaemon(t, MockRefineHooks{})
		defer md.Close()

		// Simulate one failed review for HEAD
		md.State.reviews[headSHA] = &storage.Review{
			ID: 1, JobID: 7, Output: "**Bug found**: fail", Closed: false,
		}

		// Write repo config to test config resolution fallback
		repoConfigPath := filepath.Join(repoDir, ".roborev.toml")
		err := os.WriteFile(repoConfigPath, []byte(`
refine_agent = "config-agent"
refine_model = "config-model"
refine_reasoning = "thorough"
refine_backup_agent = "config-backup-agent"
`), 0644)
		if err != nil {
			t.Fatal(err)
		}

		// Commit the config so the working tree is clean for runRefine
		gitCmd := exec.Command("git", "add", ".roborev.toml")
		gitCmd.Dir = repoDir
		if err := gitCmd.Run(); err != nil {
			t.Fatal(err)
		}
		gitCmd = exec.Command("git", "commit", "-m", "add config")
		gitCmd.Dir = repoDir
		// mock user config for git commit if needed
		gitCmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com")
		if err := gitCmd.Run(); err != nil {
			t.Fatal(err)
		}

		var capturedAgent string
		var capturedReasoning agent.ReasoningLevel
		var capturedBackup string

		factory := func(_ *config.Config, resolvedAgent string, reasoningLevel agent.ReasoningLevel, backupAgent string) (agent.Agent, error) {
			capturedAgent = resolvedAgent
			capturedReasoning = reasoningLevel
			capturedBackup = backupAgent
			return mockAgent, nil
		}

		opts := refineOptions{
			// Empty explicit values to trigger fallback
			agentName:     "",
			model:         "",
			reasoning:     "",
			agentFactory:  factory,
			quiet:         true,
			maxIterations: 1,
		}

		err = runRefine(&cobra.Command{}, ctx, opts)
		if err == nil || !strings.Contains(err.Error(), "max iterations (1) reached") {
			t.Fatalf("expected max iterations error, got: %v", err)
		}

		if capturedAgent != "config-agent" {
			t.Errorf("expected agent 'config-agent', got %q", capturedAgent)
		}
		if capturedReasoning != agent.ReasoningLevel("thorough") {
			t.Errorf("expected reasoning 'thorough', got %q", capturedReasoning)
		}
		if capturedBackup != "config-backup-agent" {
			t.Errorf("expected backup agent 'config-backup-agent', got %q", capturedBackup)
		}
		if len(mockAgent.withModelCalls) == 0 || mockAgent.withModelCalls[len(mockAgent.withModelCalls)-1] != "config-model" {
			t.Errorf("expected WithModel('config-model') to be called, got calls: %v", mockAgent.withModelCalls)
		}
	})
}
