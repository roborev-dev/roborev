package agent

import (
	"context"
	"errors"
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
					t.Errorf("buildArgs() missing expected flag %q, args: %v", flag, args)
				}
			}

			for _, flag := range tt.wantMissingFlags {
				if slices.Contains(args, flag) {
					t.Errorf("buildArgs() contains unexpected flag %q, args: %v", flag, args)
				}
			}

			// Verify stdin marker "-" is at the end (after all flags)
			if args[len(args)-1] != "-" {
				t.Errorf("expected stdin marker '-' at end of args, got %v", args)
			}
		})
	}
}

func TestCodexSupportsDangerousFlagAllowsNonZeroHelp(t *testing.T) {
	cmdPath := writeTempCommand(t, "#!/bin/sh\necho \"usage "+codexDangerousFlag+"\"; exit 1\n")

	supported, err := codexSupportsDangerousFlag(context.Background(), cmdPath)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !supported {
		t.Fatalf("expected dangerous flag support, got false")
	}
}

func TestCodexReviewUnsafeMissingFlagErrors(t *testing.T) {
	a, _ := setupMockCodex(t, true, MockCLIOpts{
		HelpOutput: "usage",
	})
	_, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("expected unsupported flag error, got %v", err)
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
		t.Fatalf("expected no error, got %v", err)
	}

	args, err := os.ReadFile(mock.ArgsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if !strings.Contains(string(args), codexAutoApproveFlag) {
		t.Fatalf("expected %s in args, got %s", codexAutoApproveFlag, strings.TrimSpace(string(args)))
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
			w := newSyncWriter(&buf) // Always initialize

			result, err := a.parseStreamJSON(strings.NewReader(tt.input), w)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("parseStreamJSON() error = %v, wantErr %v", err, tt.wantErr)
				}
				if result != "" {
					t.Errorf("parseStreamJSON() result = %q, want empty string on error", result)
				}
			} else if err != nil {
				t.Fatalf("parseStreamJSON() unexpected error: %v", err)
			}

			if tt.notWantErr != nil && errors.Is(err, tt.notWantErr) {
				t.Errorf("got unwanted error: %v", err)
			}

			if err == nil && result != tt.want {
				t.Errorf("parseStreamJSON() = %q, want %q", result, tt.want)
			}

			if tt.wantWriterContent != "" && !strings.Contains(buf.String(), tt.wantWriterContent) {
				t.Errorf("writer output = %q, expected to contain %q", buf.String(), tt.wantWriterContent)
			}
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
		t.Fatalf("expected no error, got %v", err)
	}

	received, err := os.ReadFile(mock.StdinFile)
	if err != nil {
		t.Fatalf("read stdin file: %v", err)
	}
	if string(received) != testPrompt {
		t.Fatalf("prompt not piped correctly via stdin\nexpected: %q\ngot: %q", testPrompt, string(received))
	}
}

func TestCodexReviewNoValidJSONReturnsError(t *testing.T) {
	a, _ := setupMockCodex(t, false, MockCLIOpts{
		HelpOutput:  "usage " + codexAutoApproveFlag,
		StdoutLines: []string{"plain text output"},
	})

	_, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "did not emit valid --json events") {
		t.Fatalf("expected compatibility error, got %v", err)
	}
	if !errors.Is(err, errNoCodexJSON) {
		t.Fatalf("expected wrapped errNoCodexJSON, got %v", err)
	}
}
