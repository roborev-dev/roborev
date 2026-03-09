package agent

import (
	"context"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
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

func TestCodexReviewTimeoutClosesStdoutPipe(t *testing.T) {
	skipIfWindows(t)

	prevWaitDelay := subprocessWaitDelay
	subprocessWaitDelay = 20 * time.Millisecond
	t.Cleanup(func() {
		subprocessWaitDelay = prevWaitDelay
	})

	cmdPath := writeTempCommand(t, `#!/bin/sh
if [ "$1" = "--help" ]; then
  echo "usage `+codexAutoApproveFlag+`"
  exit 0
fi
(sleep 2) &
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"partial"}}'
exit 0
`)

	a := NewCodexAgent(cmdPath)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := a.Review(ctx, t.TempDir(), "deadbeef", "prompt", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		require.NoError(t, err, "expected context deadline exceeded, got %v")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		require.LessOrEqual(t, elapsed, time.Second, "Review hung for %v after timeout", elapsed)
	}
}

func TestCodexReviewWithSessionResumePassesResumeArgs(t *testing.T) {
	a, mock := setupMockCodex(t, false, MockCLIOpts{
		HelpOutput:  "usage " + codexAutoApproveFlag,
		CaptureArgs: true,
		StdoutLines: []string{
			`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`,
		},
	})

	a = a.WithSessionID("session-123").(*CodexAgent)
	if _, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	args := readMockArgs(t, mock.ArgsFile)
	if len(args) < 4 || args[0] != "exec" || args[1] != "resume" {
		t.Fatalf("expected exec resume invocation, got %v", args)
	}
	assertContainsArg(t, args, "session-123")
}

func TestCodexReviewTimeoutClosesStdoutPipe(t *testing.T) {
	skipIfWindows(t)

	prevWaitDelay := subprocessWaitDelay
	subprocessWaitDelay = 20 * time.Millisecond
	t.Cleanup(func() {
		subprocessWaitDelay = prevWaitDelay
	})

	cmdPath := writeTempCommand(t, `#!/bin/sh
if [ "$1" = "--help" ]; then
  echo "usage `+codexAutoApproveFlag+`"
  exit 0
fi
(sleep 2) &
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"partial"}}'
exit 0
`)

	a := NewCodexAgent(cmdPath)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := a.Review(ctx, t.TempDir(), "deadbeef", "prompt", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Review hung for %v after timeout", elapsed)
	}
}

func TestCodexParseStreamJSON(t *testing.T) {
	a := NewCodexAgent("codex")

	const (
		jsonThreadStarted = `{"type":"thread.started","thread_id":"abc123"}`
		jsonTurnStarted   = `{"type":"turn.started"}`
		jsonTurnCompleted = `{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":50}}`
	)

	tests := []struct {
		name              string
		input             string
		want              string
		wantErr           error
		notWantErr        error
		wantWriterContent string
	}{
		{
			name: "AggregatesAgentMessages",
			input: buildStream(
				jsonThreadStarted,
				jsonTurnStarted,
				`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"First message"}}`,
				`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Final review result"}}`,
				jsonTurnCompleted,
			),
			want: "First message\nFinal review result",
		},
		{
			name: "AggregatesByItemID",
			input: buildStream(
				`{"type":"item.updated","item":{"id":"item_1","type":"agent_message","text":"Draft"}}`,
				`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Second message"}}`,
				`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Final first message"}}`,
			),
			want: "Final first message\nSecond message",
		},
		{
			name:  "EmptyStreamReturnsEmpty",
			input: buildStream(jsonThreadStarted, jsonTurnStarted, `{"type":"turn.completed","usage":{}}`),
			want:  "",
		},
		{
			name:       "TurnFailedReturnsError",
			input:      buildStream(`{"type":"turn.failed","error":{"message":"something broke"}}`),
			wantErr:    errCodexStreamFailed,
			notWantErr: errNoCodexJSON,
		},
		{
			name:       "ErrorEventReturnsError",
			input:      buildStream(`{"type":"error","message":"stream error"}`),
			wantErr:    errCodexStreamFailed,
			notWantErr: errNoCodexJSON,
		},
		{
			name: "IgnoresNonMessageItems",
			input: buildStream(
				`{"type":"item.started","item":{"id":"cmd1","type":"command_execution","command":"bash -lc ls"}}`,
				`{"type":"item.completed","item":{"id":"cmd1","type":"command_execution","command":"bash -lc ls","exit_code":0}}`,
				`{"type":"item.completed","item":{"id":"msg1","type":"agent_message","text":"Done reviewing."}}`,
			),
			want: "Done reviewing.",
		},
		{
			name: "DropsNarrationBeforeToolCalls",
			input: buildStream(
				jsonThreadStarted,
				jsonTurnStarted,
				`{"type":"item.completed","item":{"id":"msg1","type":"agent_message","text":"Checking the relevant files before I write the review."}}`,
				`{"type":"item.started","item":{"id":"cmd1","type":"command_execution","command":"sh -lc sed ..."}}`,
				`{"type":"item.completed","item":{"id":"cmd1","type":"command_execution","command":"sh -lc sed ...","exit_code":0}}`,
				`{"type":"item.completed","item":{"id":"msg2","type":"agent_message","text":"## Review Findings\n- **Severity**: Low; **Problem**: Final finding."}}`,
				jsonTurnCompleted,
			),
			want: "## Review Findings\n- **Severity**: Low; **Problem**: Final finding.",
		},
		{
			name: "PrefersFinalPostToolSegment",
			input: buildStream(
				jsonThreadStarted,
				jsonTurnStarted,
				`{"type":"item.completed","item":{"id":"msg1","type":"agent_message","text":"## Review Findings\n- **Severity**: Low; **Problem**: Earlier provisional finding."}}`,
				`{"type":"item.started","item":{"id":"cmd1","type":"command_execution","command":"sh -lc rg ..."}}`,
				`{"type":"item.completed","item":{"id":"cmd1","type":"command_execution","command":"sh -lc rg ...","exit_code":0}}`,
				`{"type":"item.completed","item":{"id":"msg2","type":"agent_message","text":"## Review Findings\n- **Severity**: Medium; **Problem**: Final persisted finding."}}`,
				jsonTurnCompleted,
			),
			want: "## Review Findings\n- **Severity**: Medium; **Problem**: Final persisted finding.",
		},
		{
			name:              "StreamsToWriter",
			input:             buildStream(`{"type":"item.completed","item":{"type":"agent_message","text":"hello"}}`),
			want:              "hello",
			wantWriterContent: "agent_message",
		},
		{
			name:    "NoValidJSONReturnsError",
			input:   buildStream("plain text output", "another plain text line"),
			wantErr: errNoCodexJSON,
		},
		{
			name: "JSONWithoutCodexEventTypeReturnsError",
			input: buildStream(
				`{"foo":"bar"}`,
				`{"type":""}`,
				`{"type":"foo.bar"}`,
			),
			wantErr: errNoCodexJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			w := newSyncWriter(&buf)

			result, err := a.parseStreamJSON(strings.NewReader(tt.input), w)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "parseStreamJSON() error = %v, wantErr %v", err, tt.wantErr)
				require.Empty(t, result, "unexpected condition")
			} else {
				require.NoError(t, err)
			}

			if tt.notWantErr != nil {
				require.NotErrorIs(t, err, tt.notWantErr, "got unwanted error: %v", err)
			}

			assert.False(t, err == nil && result != tt.want, "unexpected condition")

			assert.False(t, tt.wantWriterContent != "" && !strings.Contains(buf.String(), tt.wantWriterContent), "unexpected condition")
		})
	}
}

func TestCodexReviewPipesPromptViaStdin(t *testing.T) {
	a, mock := setupMockCodex(t, false, MockCLIOpts{
		HelpOutput:   "usage " + codexAutoApproveFlag,
		CaptureStdin: true,
		StdoutLines: []string{
			`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`,
		},
	})

	testPrompt := "This is a test prompt with special chars: <>&\nand newlines"
	if _, err := a.Review(context.Background(), t.TempDir(), "deadbeef", testPrompt, nil); err != nil {
		require.NoError(t, err)
	}

	received, err := os.ReadFile(mock.StdinFile)
	require.NoError(t, err, "read stdin file: %v")
	require.Equal(t, testPrompt, string(received), "prompt not piped correctly via stdin\nexpected: %q\ngot: %q", testPrompt, string(received))

}

func TestCodexReviewNoValidJSONReturnsError(t *testing.T) {
	a, _ := setupMockCodex(t, false, MockCLIOpts{
		HelpOutput:  "usage " + codexAutoApproveFlag,
		StdoutLines: []string{"plain text output"},
	})

	_, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil)
	require.Error(t, err)

	if !strings.Contains(err.Error(), "did not emit valid --json events") {
		require.Failf(t, "unexpected error", "%v", err)
	}
	if !errors.Is(err, errNoCodexJSON) {
		require.Failf(t, "unexpected error", "expected wrapped errNoCodexJSON, got %v", err)
	}
}
