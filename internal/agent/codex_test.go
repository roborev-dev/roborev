package agent

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"slices"
	"strings"
	"testing"
)

func setupMockCodex(t *testing.T, unsafe bool, opts MockCLIOpts) (*CodexAgent, *MockCLIResult) {
	withUnsafeAgents(t, unsafe)
	mock := mockAgentCLI(t, opts)
	return NewCodexAgent(mock.CmdPath), mock
}

func buildStream(events ...string) string {
	return strings.Join(events, "\n") + "\n"
}

func TestCodex_buildArgs(t *testing.T) {
	a := NewCodexAgent("codex")

	tests := []struct {
		name             string
		agentic          bool
		autoApprove      bool
		wantFlags        []string
		wantMissingFlags []string
	}{
		{
			name:             "NonAgenticAutoApprove",
			agentic:          false,
			autoApprove:      true,
			wantFlags:        []string{codexAutoApproveFlag, "--json"},
			wantMissingFlags: []string{codexDangerousFlag},
		},
		{
			name:             "AgenticNoAutoApprove",
			agentic:          true,
			autoApprove:      false,
			wantFlags:        []string{codexDangerousFlag, "--json"},
			wantMissingFlags: []string{codexAutoApproveFlag},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := a.buildArgs("/repo", tt.agentic, tt.autoApprove)

			for _, flag := range tt.wantFlags {
				if !slices.Contains(args, flag) {
					assert.Contains(t, args, flag, "buildArgs() missing expected flag %q, args: %v", flag, args)
				}
			}

			for _, flag := range tt.wantMissingFlags {
				if slices.Contains(args, flag) {
					assert.NotContains(t, args, flag, "buildArgs() contains unexpected flag %q, args: %v", flag, args)
				}
			}
			assert.Equal(t, "-", args[len(args)-1], "expected stdin marker '-' at end of args, got %v", args)

		})
	}
}

func TestCodexBuildArgsWithSessionResume(t *testing.T) {
	a := NewCodexAgent("codex").WithSessionID("session-123").(*CodexAgent)

	args := a.buildArgs("/repo", false, true)

	if len(args) < 3 || args[0] != "exec" || args[1] != "resume" {
		t.Fatalf("expected exec resume prefix, got %v", args)
	}
	assertContainsArg(t, args, "--json")
	assertContainsArg(t, args, "session-123")
	if args[len(args)-1] != "-" {
		t.Fatalf("expected stdin marker '-' at end of args, got %v", args)
	}
}

func TestCodexBuildArgsRejectsInvalidSessionResume(t *testing.T) {
	a := NewCodexAgent("codex").WithSessionID("-bad-session").(*CodexAgent)

	args := a.buildArgs("/repo", false, true)

	if len(args) < 2 || args[0] != "exec" || args[1] == "resume" {
		t.Fatalf("expected resume subcommand to be omitted for invalid session id, got %v", args)
	}
	assertNotContainsArg(t, args, "-bad-session")
}

func TestCodexSupportsDangerousFlagAllowsNonZeroHelp(t *testing.T) {
	cmdPath := writeTempCommand(t, "#!/bin/sh\necho \"usage "+codexDangerousFlag+"\"; exit 1\n")

	supported, err := codexSupportsDangerousFlag(context.Background(), cmdPath)
	require.NoError(t, err)

	require.True(t, supported)

}

func TestCodexReviewUnsafeMissingFlagErrors(t *testing.T) {
	a, _ := setupMockCodex(t, true, MockCLIOpts{
		HelpOutput: "usage",
	})
	_, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil)
	require.Error(t, err)

	if !strings.Contains(err.Error(), "does not support") {
		require.Failf(t, "unexpected error", "%v", err)
	}
}

func TestCodexReviewAlwaysAddsAutoApprove(t *testing.T) {
	a, mock := setupMockCodex(t, false, MockCLIOpts{
		HelpOutput:  "usage " + codexAutoApproveFlag,
		CaptureArgs: true,
		StdoutLines: []string{
			`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`,
		},
	})

	if _, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil); err != nil {
		require.NoError(t, err)
	}

	args, err := os.ReadFile(mock.ArgsFile)
	require.NoError(t, err, "read args: %v")

	if !strings.Contains(string(args), codexAutoApproveFlag) {
		require.Contains(t, strings.TrimSpace(string(args)), codexAutoApproveFlag, "expected %s in args, got %s", codexAutoApproveFlag, strings.TrimSpace(string(args)))
	}
}
