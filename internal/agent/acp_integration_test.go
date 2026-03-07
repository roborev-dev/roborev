//go:build integration && acp

package agent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	acpIntegrationEnableEnv      = "ROBOREV_RUN_ACP_INTEGRATION"
	acpIntegrationCommandEnv     = "ROBOREV_ACP_TEST_COMMAND"
	acpIntegrationArgsEnv        = "ROBOREV_ACP_TEST_ARGS"
	acpIntegrationDisableModeEnv = "ROBOREV_ACP_TEST_DISABLE_MODE"
	acpIntegrationModeEnv        = "ROBOREV_ACP_TEST_MODE"
	acpIntegrationModelEnv       = "ROBOREV_ACP_TEST_MODEL"
)

func envBool(key string) bool {
	val := strings.TrimSpace(os.Getenv(key))
	return strings.EqualFold(val, "1") || strings.EqualFold(val, "true")
}

func envString(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func setupDummyGitRepo(t *testing.T) (repoPath string, commitSHA string) {
	t.Helper()
	repoPath = t.TempDir()

	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("# ACP Integration Test\n"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	gitRun := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	gitRun("init")
	disabledHooksDir := filepath.Join(repoPath, ".git", "hooks-disabled")
	if err := os.MkdirAll(disabledHooksDir, 0o755); err != nil {
		t.Fatalf("failed to create disabled hooks dir: %v", err)
	}

	gitRun("config", "user.email", "acp-integration@example.com")
	gitRun("config", "user.name", "ACP Integration")
	gitRun("add", "README.md")
	gitRun("-c", "commit.gpgsign=false", "-c", "core.hooksPath="+disabledHooksDir, "commit", "--no-verify", "-m", "seed")

	return repoPath, gitRun("rev-parse", "HEAD")
}

// TestACPReviewViaExternalAdapter exercises ACP end-to-end against a real wrapper command.
//
// Example:
//
//	make test-acp-integration
//	make test-acp-integration-codex
//	make test-acp-integration-claude
//	make test-acp-integration-gemini
//
// Override command if needed:
//
//	make test-acp-integration ACP_TEST_COMMAND=codex-acp
//	make test-acp-integration ACP_TEST_DISABLE_MODE=1
func TestACPReviewViaExternalAdapter(t *testing.T) {
	if os.Getenv(acpIntegrationEnableEnv) != "1" {
		t.Skipf("set %s=1 to run ACP integration tests", acpIntegrationEnableEnv)
	}

	command := envString(acpIntegrationCommandEnv)
	if command == "" {
		command = defaultACPCommand
	}
	args := strings.Fields(envString(acpIntegrationArgsEnv))

	if _, err := exec.LookPath(command); err != nil {
		t.Fatalf("ACP command %q not found in PATH: %v", command, err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for ACP integration smoke test")
	}

	repoPath, commitSHA := setupDummyGitRepo(t)
	if commitSHA == "" {
		t.Fatal("failed to resolve HEAD commit SHA for integration test")
	}

	agent := NewACPAgent(command)
	agent.Args = args
	agent.Timeout = 2 * time.Minute
	if envBool(acpIntegrationDisableModeEnv) {
		agent.Mode = ""
	}
	if mode := envString(acpIntegrationModeEnv); mode != "" {
		agent.Mode = mode
	}
	if model := envString(acpIntegrationModelEnv); model != "" {
		agent.Model = model
	}

	var streamOutput bytes.Buffer
	result, err := agent.Review(
		context.Background(),
		repoPath,
		commitSHA,
		"Connectivity smoke test: respond with exactly 'ACP_OK' after reading README.md. Do not run roborev commands.",
		&streamOutput,
	)
	if err != nil {
		t.Fatalf("ACP review failed: %v\nstream output:\n%s", err, streamOutput.String())
	}

	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		t.Fatalf("expected non-empty ACP result; stream output:\n%s", streamOutput.String())
	}
	if !strings.Contains(trimmed, "ACP_OK") {
		t.Fatalf("expected ACP_OK marker in result, got:\n%s", trimmed)
	}
}
